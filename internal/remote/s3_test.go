package remote

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// mockS3 is a tiny in-memory S3 sufficient to exercise the client's PUT, GET,
// HEAD and ListObjectsV2 paths and to assert the request is SigV4 signed.
type mockS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	bucket  string
	sawAuth bool
}

func (m *mockS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		http.Error(w, "unsigned", 403)
		return
	}
	if r.Header.Get("X-Amz-Content-Sha256") == "" || r.Header.Get("X-Amz-Date") == "" {
		http.Error(w, "missing sigv4 headers", 403)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sawAuth = true
	// path-style: /bucket/key
	path := strings.TrimPrefix(r.URL.Path, "/"+m.bucket)
	path = strings.TrimPrefix(path, "/")
	switch r.Method {
	case "PUT":
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		m.objects[path] = b
		w.WriteHeader(200)
	case "HEAD":
		if _, ok := m.objects[path]; ok {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
	case "GET":
		if r.URL.Query().Get("list-type") == "2" {
			m.list(w, r.URL.Query().Get("prefix"))
			return
		}
		if b, ok := m.objects[path]; ok {
			w.Write(b)
		} else {
			w.WriteHeader(404)
		}
	default:
		w.WriteHeader(405)
	}
}

func (m *mockS3) list(w http.ResponseWriter, prefix string) {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
	for _, k := range keys {
		fmt.Fprintf(&b, "<Contents><Key>%s</Key></Contents>", k)
	}
	b.WriteString("</ListBucketResult>")
	w.Write([]byte(b.String()))
}

func newMockS3(t *testing.T) (*S3, *mockS3) {
	t.Helper()
	m := &mockS3{objects: map[string][]byte{}, bucket: "ckpts"}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	s3, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "ckpts", Region: "us-east-1",
		AccessKey: "AKIAEXAMPLE", SecretKey: "secret", PathStyle: true, Prefix: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	return s3, m
}

func TestS3PutGetHasList(t *testing.T) {
	s3, m := newMockS3(t)
	if err := s3.Put("blobs/abc", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := s3.Get("blobs/abc")
	if err != nil || string(got) != "hello" {
		t.Fatalf("get: %q %v", got, err)
	}
	if ok, _ := s3.Has("blobs/abc"); !ok {
		t.Fatal("Has should be true")
	}
	if ok, _ := s3.Has("blobs/missing"); ok {
		t.Fatal("Has should be false for a missing key")
	}
	if _, err := s3.Get("blobs/missing"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	s3.Put("checkpoints/ck-1.json", []byte("{}"))
	keys, err := s3.List("checkpoints/")
	if err != nil || len(keys) != 1 || keys[0] != "checkpoints/ck-1.json" {
		t.Fatalf("list: %v %v", keys, err)
	}
	// the prefix was applied and stripped, and the request was signed
	if !m.sawAuth {
		t.Fatal("requests must be SigV4 signed")
	}
	if _, ok := m.objects["team-a/blobs/abc"]; !ok {
		t.Fatalf("prefix not applied on the wire: %v", keysOf(m))
	}
}

func keysOf(m *mockS3) []string {
	var k []string
	for key := range m.objects {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}
