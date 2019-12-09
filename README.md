# httpstream

httpstream provides parallel download of a single http file that can be streamed incrementally.
It should be used to download large files when there the download content needs to be
processed incrementally without creating a local copy of the entire file.

It uses byte range gets to download the file concurrently and a priority queue to order
the downloads as they arrive so as to present a streaming API (io.Reader) to its clients.


```go
    dl := httpstream.New(ctx, url)
    _, err := io.Copy(os.Stderr, dl)
```
