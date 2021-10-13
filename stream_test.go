// Copyright 2020 Cosmos Nicolaou. All rights reserved.
// Use of this source code is governed by the Apache-2.0
// license that can be found in the LICENSE file.
package httpstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
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

func init() {
	for k, v := range servingData {
		if err := os.WriteFile(k, v, 0600); err != nil {
			panic(err)
		}
	}
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
	defer srv.CloseClientConnections()
	for _, chunksize := range []int64{10, 100, 1024, 2048} {
		for _, tc := range []struct {
			name    string
			sha1Sum string
			md5Sum  string
		}{
			{
				"0B",
				"da39a3ee5e6b4b0d3255bfef95601890afd80709", "d41d8cd98f00b204e9800998ecf8427e",
			},
			{
				"100B",
				"6013a010a4576238ccd19dd628ef06b7a4000c98", "02c731532815604ce3dac8f04df9b0b6",
			},
			{
				"1K",
				"15074d9845839a82c97e2e7097e7d40ce5395f4d", "be50caf3e170e7a0f218d2f01e77b565",
			},
			{
				"1M",
				"99d92f7ecb8e9454e5d14196476d3d2db4754e4c", "612efa708a81ab8abd4c0c7cc31956d5",
			},
		} {
			name := fmt.Sprintf("name: %s chunksize: %v", tc.name, chunksize)
			opts := []Option{
				Chunksize(chunksize),
				Verbose(testing.Verbose()),
			}
			url := srv.URL + "/" + tc.name
			dl := New(ctx, url, opts...)
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
			dl.Finish()
			t.Logf("download done: %v", name)

			for _, chk := range []struct {
				opts []Option
				ok   bool
			}{
				{[]Option{VerifySHA1(tc.sha1Sum)}, true},
				{[]Option{VerifyMD5(tc.md5Sum)}, true},
				{[]Option{VerifySHA1(tc.sha1Sum), VerifyMD5(tc.md5Sum)}, true},
				{[]Option{VerifySHA1("badsum")}, false},
				{[]Option{VerifyMD5("badsum")}, false},
				{[]Option{VerifySHA1("badsum"), VerifyMD5("badsum")}, false},
			} {
				nopts := append(opts, chk.opts...)
				dl := New(ctx, url, nopts...)
				_, err := io.ReadAll(dl)
				if chk.ok {
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
					continue
				}
				if err == nil {
					t.Errorf("expected an error: %v", err)
				}
				if got, want := err.Error(), "checksum mismatch"; !strings.Contains(got, want) {
					t.Errorf("error %v does not contain %v", got, want)
				}
				dl.Finish()
			}
			t.Logf("download checks: %v", name)
		}
	}
	fmt.Printf("all done\n")
}

func hang(res http.ResponseWriter, req *http.Request) {
	if _, ok := req.Header["Range"]; ok {
		time.Sleep(time.Hour)
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
		{"drop", dropClientConnections()},
	} {
		ctx, cancel := context.WithCancel(ctx)
		dl := New(ctx, tc.srv.URL+"/anything", Verbose(testing.Verbose()))
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
