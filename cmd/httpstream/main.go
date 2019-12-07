package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"

	"github.com/cosnicolaou/httpstream"
	"github.com/grailbio/base/file"
	"github.com/grailbio/base/must"
	"v.io/x/lib/cmd/flagvar"
)

var commandline struct {
	URL    string `cmd:"url,,url to be downloaded"`
	Output string `cmd:"output,,output file or s3 prefix"`
}

func init() {
	must.Nil(flagvar.RegisterFlagsInStruct(flag.CommandLine, "cmd", &commandline,
		nil, nil))
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
	var out io.Writer

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
	} else {
		out = os.Stdout
	}

	dl, err := httpstream.New(ctx, commandline.URL)
	if err != nil {
		log.Fatalf("New: %v: %v", commandline.URL, err)
	}
	fmt.Printf("%#v\n", dl)
	if _, err := io.Copy(out, dl.Reader()); err != nil {
		log.Fatalf("copy failed")
	}
	if err := dl.Finish(); err != nil {
		log.Fatalf("finish failed")
	}
}
