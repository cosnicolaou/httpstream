package httpstream

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"
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

func TestStream(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(servePredictable))
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
			opts := []Option{Chunksize(chunksize)}
			if testing.Verbose() {
				opts = append(opts, Verbose())
			}
			dl := New(ctx, srv.URL+"/"+tc.name, opts...)
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
			// make sure all go-routines finish.
			dl.wg.Wait()
			t.Logf("done: %v", name)
		}
	}
}

func hang(res http.ResponseWriter, req *http.Request) {
	if _, ok := req.Header["Range"]; ok {
		time.Sleep(time.Hour)
		return
	}
	body := []byte("hello")
	http.ServeContent(res, req, "ok", time.Now(), bytes.NewReader(body))
}

func causeBackoff(res http.ResponseWriter, req *http.Request) {
	if _, ok := req.Header["Range"]; ok {
		res.WriteHeader(http.StatusInternalServerError)
		return
	}
	body := []byte("hello")
	http.ServeContent(res, req, "ok", time.Now(), bytes.NewReader(body))
}

type wrapper struct {
	srv *httptest.Server
}

func (wr *wrapper) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	if _, ok := req.Header["Range"]; ok {
		wr.srv.CloseClientConnections()
		return
	}
	time.Sleep(time.Second)
	body := []byte("hello")
	http.ServeContent(res, req, "ok", time.Now(), bytes.NewReader(body))
}

func dropClientConnections() *httptest.Server {
	wr := &wrapper{}
	srv := httptest.NewServer(wr)
	wr.srv = srv
	return srv
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		srv  *httptest.Server
	}{
		{"hang", httptest.NewServer(http.HandlerFunc(hang))},
		{"backoff", dropClientConnections()},
	} {
		ctx, cancel := context.WithCancel(ctx)
		dl := New(ctx, tc.srv.URL+"/anything", Verbose())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		_, err := ioutil.ReadAll(dl)
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("cancel did not succeed: %v", err)
		}
	}
}
