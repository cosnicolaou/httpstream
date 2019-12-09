package httpstream

import (
	"bytes"
	"container/heap"
	"encoding/hex"
	"fmt"
	"hash"
	"time"
)

// Progress represents a progress update
type Progress struct {
	Size int
}

type blockDesc struct {
	order    int
	duration time.Duration
	buf      *bytes.Buffer
	err      error
}

type blockHeap []*blockDesc

func (h blockHeap) Len() int           { return len(h) }
func (h blockHeap) Less(i, j int) bool { return h[i].order < h[j].order }
func (h blockHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *blockHeap) Push(x interface{}) {
	// Push and Pop use pointer receivers because they modify the slice's length,
	// not just its contents.
	*h = append(*h, x.(*blockDesc))
}

func (h *blockHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func (dl *Downloader) validateChecksums() error {
	for _, chk := range []struct {
		name, value string
		h           hash.Hash
	}{
		{"sha1", dl.sha1Sum, dl.sha1},
		{"md5", dl.md5Sum, dl.md5},
	} {
		if chk.h == nil {
			continue
		}
		sum := chk.h.Sum(nil)
		got, want := hex.EncodeToString(sum[:]), chk.value
		dl.trace("checking: %v: %v =? %v", chk.name, got, want)
		if got != want {
			return fmt.Errorf("checksum mismatch %v:%v != %v:%v", chk.name, got, chk.name, want)
		}
	}
	return nil
}

func (dl *Downloader) assemble(ch <-chan *blockDesc) error {
	expected := 0
	for {
		select {
		case block := <-ch:
			dl.trace("assemble: %v expected %v", block, expected)
			if block != nil {
				heap.Push(dl.heap, block)
			}
			for len(*dl.heap) > 0 {
				min := (*dl.heap)[0]
				if min.order != expected {
					break
				}
				if err := min.err; err != nil {
					dl.bufPool.Put(min.buf)
					return err
				}
				n, err := dl.wr.Write(min.buf.Bytes())
				dl.bufPool.Put(min.buf)
				if err != nil {
					return err
				}
				heap.Remove(dl.heap, 0)
				if dl.updatesCh != nil {
					dl.updatesCh <- Progress{
						Size: n,
					}
				}
				expected++
			}
			if block == nil && len(*dl.heap) == 0 {
				dl.trace("assemble: done")
				if err := dl.validateChecksums(); err != nil {
					return err
				}
				return nil
			}
		case <-dl.ctx.Done():
			dl.trace("assemble: ctx done %v", dl.ctx.Err())
		}
	}
}
