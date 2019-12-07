package httpstream

import (
	"bytes"
	"container/heap"
	"time"
)

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

func (dl *Downloader) assemble(ch <-chan *blockDesc) {
	defer dl.pwr.Close()
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
					dl.pwr.CloseWithError(err)
					return
				}
				_, err := dl.pwr.Write(min.buf.Bytes())
				dl.bufPool.Put(min.buf)
				if err != nil {
					dl.pwr.CloseWithError(err)
					return
				}
				heap.Remove(dl.heap, 0)
				/*
					if dc.progressCh != nil {
						dc.progressCh <- Progress{
							Duration:   min.duration,
							Block:      min.order,
							CRC:        min.crc,
							Compressed: len(min.block),
							Size:       len(min.data),
						}
					}*/
				expected++
			}
			if block == nil && len(*dl.heap) == 0 {
				dl.trace("assemble: done")
				return
			}
		case <-dl.ctx.Done():
			dl.trace("assemble: ctx done %v", dl.ctx.Err())
		}
	}
}
