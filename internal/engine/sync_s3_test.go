package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/numinous-technology/vitvm/internal/remote"
)

// A minimal in-memory S3 so the checkpoint push/pull/fork path is exercised
// over the real S3 client (SigV4 signing and all), not only the directory
// store. No AWS, no network beyond loopback.
type s3mock struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *s3mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		http.Error(w, "unsigned", 403)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/bkt"), "/")
	switch r.Method {
	case "PUT":
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		m.objs[key] = b
		w.WriteHeader(200)
	case "HEAD":
		if _, ok := m.objs[key]; !ok {
			w.WriteHeader(404)
		}
	case "GET":
		if r.URL.Query().Get("list-type") == "2" {
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
			for k := range m.objs {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
					fmt.Fprintf(&b, "<Contents><Key>%s</Key></Contents>", k)
				}
			}
			b.WriteString("</ListBucketResult>")
			w.Write([]byte(b.String()))
			return
		}
		if v, ok := m.objs[key]; ok {
			w.Write(v)
		} else {
			w.WriteHeader(404)
		}
	}
	_ = sort.Strings
}

func TestPushPullForkOverS3(t *testing.T) {
	m := &s3mock{objs: map[string][]byte{}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	store, err := remote.NewS3(remote.S3Config{Endpoint: srv.URL, Bucket: "bkt", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	pushPullFork(t, store)
}
