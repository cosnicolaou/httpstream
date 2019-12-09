package httpstream

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"hash"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/cenkalti/backoff"
)

type options struct {
	concurrency int
	chunksize   int64
	verbose     bool
	updatesCh   chan<- Progress
	sha1        string
	md5         string
}

// Option represen ts an option to NewDownloader.
type Option func(*options)

// Concurrency sets the degree of concurrency to use, that is,
// the number of download threads.
func Concurrency(n int) Option {
	return func(o *options) {
		o.concurrency = n
	}
}

// Chunksize sets the size of each byte range chunk to be requested.
func Chunksize(n int64) Option {
	return func(o *options) {
		o.chunksize = n
	}
}

// Verbose controls verbose logging.
func Verbose(v bool) Option {
	return func(o *options) {
		o.verbose = v
	}

}

// SendUpdates enables sending progreess updates to the specified channel.
func SendUpdates(ch chan<- Progress) Option {
	return func(o *options) {
		o.updatesCh = ch
	}
}

func VerifySHA1(sum string) Option {
	return func(o *options) {
		o.sha1 = sum
	}
}

func VerifyMD5(sum string) Option {
	return func(o *options) {
		o.md5 = sum
	}
}

// Downloader represents a concurrent, streaming, http downloader.
type Downloader struct {
	ctx             context.Context
	verbose         bool
	url             string
	size            int64
	transport       http.RoundTripper
	bufPool         sync.Pool
	prd             *io.PipeReader
	pwr             *io.PipeWriter
	wr              io.Writer
	rangeCh         chan *byteRange
	workerErrCh     chan error
	assembleCh      chan *blockDesc
	wg              sync.WaitGroup
	heap            *blockHeap
	updatesCh       chan<- Progress
	sha1, md5       hash.Hash
	sha1Sum, md5Sum string
}

var (
	defaultConcurrency     = runtime.GOMAXPROCS(-1)
	defaultChunksize       = int64(1024 * 1024)
	defaultMaxConnsPerHost = defaultConcurrency
)

type byteRange struct {
	order    int
	from, to int64
}

func generator(ctx context.Context, ch chan<- *byteRange, length, size int64) {
	start := int64(0)
	order := 0
	for ; start+size < length; start += size {
		select {
		case ch <- &byteRange{order, start, start + size - 1}:
			order++
		case <-ctx.Done():
			return
		}
	}
	if start == length {
		return
	}
	select {
	case ch <- &byteRange{order, start, length - 1}:
	case <-ctx.Done():
	}
}

func (dl *Downloader) trace(format string, args ...interface{}) {
	if dl.verbose {
		log.Printf(format, args...)
	}
}

func (dl *Downloader) get(url string, buf *bytes.Buffer, br *byteRange) error {
	client := &http.Client{Transport: dl.transport}
	b := backoff.NewExponentialBackOff()
	b.MaxInterval = 2 * time.Minute
	b.MaxElapsedTime = time.Hour
	req, err := http.NewRequestWithContext(dl.ctx, "GET", url, nil)
	req.Header["Range"] = []string{fmt.Sprintf("bytes=%d-%d", br.from, br.to)}
	if err != nil {
		return err
	}
	dl.trace("get: %v: %v..%v [%v]\n", br.order, br.from, br.to, br.to-br.from)
	for {
		backoffTime := b.NextBackOff()
		resp, err := client.Do(req)
		if err == nil {
			switch resp.StatusCode {
			case http.StatusOK, http.StatusPartialContent:
				dl.trace("%v: %v: %v", req.URL, req.Header["Range"], resp.ContentLength)
				io.Copy(buf, resp.Body)
				resp.Body.Close()
				return nil
			case http.StatusServiceUnavailable:
			default:
				dl.trace("get: %v: %v: %v: bad status: %v", req.URL, req.Header["Range"], resp.ContentLength, resp.Status)
				return fmt.Errorf("bad status code: %v", resp.Status)
			}
		}
		state := "retry with backoff"
		if backoffTime == backoff.Stop {
			// never give up, but report that that we're at the max.
			state = "at max retry backoff interval"
		}
		dl.trace("get: %v: %v: %v: %v", req.URL, req.Header["Range"], state, err)
		select {
		case <-time.After(backoffTime):
		case <-dl.ctx.Done():
			return fmt.Errorf("get: %v", dl.ctx.Err())
		}
	}
}

func (dl *Downloader) worker(in <-chan *byteRange, out chan<- *blockDesc) {
	for {
		select {
		case r := <-in:
			if r == nil {
				return
			}
			start := time.Now()
			buf := dl.bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			err := dl.get(dl.url, buf, r)
			bl := &blockDesc{
				order:    r.order,
				buf:      buf,
				duration: time.Since(start),
				err:      err,
			}
			dl.trace("worker: %v: %v\n", bl.order, bl.duration)
			out <- bl
		case <-dl.ctx.Done():
			dl.trace("worker: ctx done %v", dl.ctx.Err())
			return
		}
	}
}

// New returns a new instance of Downloader.
func New(ctx context.Context, url string, opts ...Option) *Downloader {
	o := options{
		concurrency: defaultConcurrency,
		chunksize:   defaultChunksize,
	}

	for _, fn := range opts {
		fn(&o)
	}

	// Figure out what to tune for large files served from slow
	// rate-limited sites.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   120 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          o.concurrency,
		MaxIdleConnsPerHost:   o.concurrency,
		IdleConnTimeout:       180 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 10 * time.Second,
		MaxConnsPerHost:       o.concurrency,
		ReadBufferSize:        16 * 1024,
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(url)
	if err != nil {
		dl := &Downloader{}
		dl.prd, dl.pwr = io.Pipe()
		dl.pwr.CloseWithError(fmt.Errorf("%v: %v", url, err))
		return dl
	}

	if ranges, ok := resp.Header["Accept-Ranges"]; !ok || (len(ranges) != 1 && ranges[0] != "bytes") {
		dl := &Downloader{}
		dl.prd, dl.pwr = io.Pipe()
		dl.pwr.CloseWithError(fmt.Errorf("%v does not supprt byte-range gets", url))
		return dl
	}

	nparts := int(resp.ContentLength/o.chunksize) + 1
	nworkers := int(o.concurrency)
	if nparts < nworkers {
		nworkers = nparts
	}

	dl := &Downloader{
		ctx:       ctx,
		url:       url,
		transport: transport,
		size:      resp.ContentLength,
		bufPool: sync.Pool{
			New: func() interface{} {
				return bytes.NewBuffer(make([]byte, o.chunksize))
			},
		},
		rangeCh:     make(chan *byteRange, nworkers*2),
		assembleCh:  make(chan *blockDesc, nworkers*2),
		workerErrCh: make(chan error, nworkers),
		heap:        &blockHeap{},
		verbose:     o.verbose,
		updatesCh:   o.updatesCh,
	}

	dl.prd, dl.pwr = io.Pipe()
	dl.wr = dl.pwr

	writers := []io.Writer{dl.pwr}
	if len(o.sha1) > 0 {
		dl.sha1Sum = o.sha1
		dl.sha1 = sha1.New()
		writers = append(writers, dl.sha1)
	}

	if len(o.md5) > 0 {
		dl.md5Sum = o.md5
		dl.md5 = md5.New()
		writers = append(writers, dl.md5)
	}

	if len(writers) > 1 {
		dl.wr = io.MultiWriter(writers...)
	}

	heap.Init(dl.heap)
	var generatorWg, workerWg, assembleWg sync.WaitGroup
	generatorWg.Add(1)
	workerWg.Add(nworkers)
	assembleWg.Add(1)
	dl.wg.Add(3)

	go func() {
		generator(ctx, dl.rangeCh, resp.ContentLength, o.chunksize)
		close(dl.rangeCh)
		generatorWg.Done()
		dl.wg.Done()
		dl.trace("range generator finished")
	}()

	for i := 0; i < nworkers; i++ {
		go func(w int) {
			dl.trace("worker: running %v/%v", w, nworkers)
			dl.worker(dl.rangeCh, dl.assembleCh)
			workerWg.Done()
			dl.trace("worker: done %v/%v: %v", w, nworkers, err)
		}(i)
	}

	go func() {
		workerWg.Wait()
		close(dl.assembleCh)
		dl.wg.Done()
		dl.trace("workers: finished")
	}()

	go func() {
		err := dl.assemble(dl.assembleCh)
		dl.pwr.CloseWithError(err)
		assembleWg.Done()
		dl.wg.Done()
		dl.trace("assembler: finished")
	}()
	return dl
}

// Read implements io.Reader.
func (dl *Downloader) Read(buf []byte) (int, error) {
	return dl.prd.Read(buf)
}

// Reader returns an io.Reader.
func (dl *Downloader) Reader() io.Reader {
	return dl.prd
}

// ContentLength returns the content length header for the file being
// downloaded.
func (dl *Downloader) ContentLength() int64 {
	return dl.size
}
