package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"

	"github.com/cosnicolaou/httpstream"
	"github.com/grailbio/base/file"
	"github.com/grailbio/base/must"
	"github.com/schollz/progressbar/v2"
	"v.io/x/lib/cmd/flagvar"
)

var commandline struct {
	URL         string `cmd:"url,,url to be downloaded"`
	Concurrency int    `cmd:"concurrency,,'concurrency for the download'"`
	Output      string `cmd:"output,,output file or s3 prefix"`
	ProgressBar bool   `cmd:"progress,true,display a progress bar"`
	Sha1        string `cmd:"sha1,,specify a sha1 to compare the download file against"`
	Verbose     bool   `cmd:"verbose,false,verbose debug/trace information"`
	RangeSize   int64  `cmd:"range-size,1048576,size of each byte-range get"`
}

func init() {
	must.Nil(flagvar.RegisterFlagsInStruct(flag.CommandLine, "cmd", &commandline,
		map[string]interface{}{
			"concurrency": runtime.GOMAXPROCS(-1),
		}, nil))
}

func progressBar(ctx context.Context, ch chan httpstream.Progress, size int64) {
	bar := progressbar.NewOptions64(size,
		progressbar.OptionSetBytes64(size),
		progressbar.OptionSetPredictTime(true))
	bar.RenderBlank()
	for {
		select {
		case p := <-ch:
			if p.Size == 0 {
				fmt.Println()
				return
			}
			bar.Add(p.Size)
		case <-ctx.Done():
			return
		}
	}
}

func OnSignal(fn func(), signals ...os.Signal) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, signals...)
	go func() {
		sig := <-sigCh
		fmt.Println("stopping on... ", sig)
		fn()
	}()
}

func main() {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	OnSignal(cancel, os.Interrupt)
	flag.Parse()
	if len(commandline.URL) == 0 {
		log.Fatal("must provide --url")
	}
	if len(commandline.Output) == 0 {
		// disable the progress bar.
		commandline.ProgressBar = false
	}

	opts := []httpstream.Option{
		httpstream.Concurrency(commandline.Concurrency),
		httpstream.Verbose(commandline.Verbose),
		httpstream.Chunksize(commandline.RangeSize),
	}
	var (
		out           io.Writer
		progressBarCh chan httpstream.Progress
		progressBarWg sync.WaitGroup
	)

	if commandline.ProgressBar {
		progressBarCh = make(chan httpstream.Progress, commandline.Concurrency)
		opts = append(opts, httpstream.SendUpdates(progressBarCh))
	}

	dl := httpstream.New(ctx, commandline.URL, opts...)

	if name := commandline.Output; len(name) > 0 {
		file, err := file.Create(ctx, name)
		if err != nil {
			log.Fatalf("failed to create %v: %v", name, err)
		}
		defer func() {
			if err := file.Close(ctx); err != nil {
				log.Fatalf("failed to close %v: %v", name, err)
			}
		}()
		out = file.Writer(ctx)
		if commandline.ProgressBar {
			progressBarWg.Add(1)
			go func() {
				progressBar(ctx, progressBarCh, dl.ContentLength())
				progressBarWg.Done()
			}()
		}
	} else {
		out = os.Stdout
	}

	var wr io.Writer
	var digester hash.Hash
	var expected string
	if expected = commandline.Sha1; len(expected) > 0 {
		digester = sha1.New()
		wr = io.MultiWriter(out, digester)
	} else {
		wr = out
	}
	if _, err := io.Copy(wr, dl); err != nil {
		log.Fatalf("copy failed: %v", err)
	}
	if digester != nil {
		digest := digester.Sum(nil)
		sum := hex.EncodeToString(digest[:])
		if sum != expected {
			log.Fatalf("checksum mismatch: %v != %v", sum, expected)
		}
	}
	close(progressBarCh)
	progressBarWg.Wait()
}
