package httpstream_test

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"time"

	"github.com/cosnicolaou/httpstream"
)

var randSource = rand.NewSource(0x1234)

func genPredictableRandomData(size int) []byte {
	gen := rand.New(randSource)
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(gen.Intn(256))
	}
	return out
}

var servingData = map[string][]byte{
	"0B":   genPredictableRandomData(0),
	"100B": genPredictableRandomData(100),
	"1K":   genPredictableRandomData(1024),
	"1M":   genPredictableRandomData(1024 * 1024),
}

func servePredictable(res http.ResponseWriter, req *http.Request) {
	name := path.Base(req.URL.Path)
	body, ok := servingData[name]
	if !ok {
		res.WriteHeader(http.StatusNotFound)
		return
	}
	http.ServeContent(res, req, name, time.Now(), bytes.NewReader(body))
}

func runServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(servePredictable))
}

func TestStream(t *testing.T) {
	ctx := context.Background()
	srv := runServer()
	defer srv.Close()
	for _, chunksize := range []int64{10, 100, 1024, 2048} {
		for _, tc := range []struct {
			name string
		}{
			{"0B"},
			{"100B"},
			{"1K"},
			{"1M"},
		} {
			name := fmt.Sprintf("name: %s chunksize: %v", tc.name, chunksize)
			opts := []httpstream.Option{httpstream.Chunksize(chunksize)}
			if testing.Verbose() {
				opts = append(opts, httpstream.Verbose())
			}
			dl, err := httpstream.New(ctx, srv.URL+"/"+tc.name, opts...)
			if err != nil {
				t.Fatalf("%v: %v", name, err)
			}
			buf, err := ioutil.ReadAll(dl)
			if err != nil {
				t.Errorf("%v: %v", name, err)
			}
			firstN := func(n int, b []byte) []byte {
				if len(b) > n {
					return b[:n]
				}
				return b
			}
			if got, want := len(buf), len(servingData[tc.name]); got != want {
				t.Errorf("%v: got %v, want %v", name, got, want)
			}
			if got, want := buf, servingData[tc.name]; !bytes.Equal(got, want) {
				t.Errorf("%v: got %v, want %v", name, firstN(10, got), firstN(10, want))
			}
			if err := dl.Finish(); err != nil {
				t.Errorf("%v: finish: %v", name, err)
			}
			t.Logf("done: %v", name)
		}
	}
}
