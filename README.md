# httpstream

httpstream provides parallel download of a single http file that can be streamed incrementally. It uses multiple http connections and 'byte range' GET calls
to do so. The downloaded portions of the file are then assembled into
their original order and streamed as the contiguous run of blocks (from the
start of the file) is received. This allows for a simple reader to consume
the data as if it were read serially. Typical use of the package is:

```go
    dl := httpstream.New(ctx, url)
    _, err := io.Copy(os.Stdout, dl)
```

A command line tool is also provided, `github.com/cosnicolaou/httpstream/cmd/httpstream`,
which can be used as follows:

``` 
$ go run ./cmd/httpstream --url=https://dumps.wikimedia.org/wikidatawiki/entities/20191202/wikidata-20191202-all.json.bz2 --output=20191202.bz2 --md5=316e7d034c50f072a1b2738ef366bd76
```

The combination of parallel download and streaming allow for significantly
reducing the latency of large file downloads run on multi-core machines that
are the first step in a more complex pipeline. For example, this package
can be used in conjunction with ```github.com/cosnicolaou/pbzip2``` to overlap
download and decompression. For the example file above, the download completes
in around 30 minutes vs 2-3 hours. As always, some degree of tuning will be required
for each site. The number of parallel connections can be controlled as can the size
of each byte range request. Different values for these can have dramatic effects
on the achieved througput. Larger values also decrease the frequency of status
updates and hence any progress bar updates.

The package provides support for:
  - verifying the md5 or sha1 of the downloaded file
  - implementing a progress bar by sending progress updates over a channel.

A planned additional feature is the ability to checkpoint and resume
a given download.


