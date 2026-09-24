package remote

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Config points at a bucket in any S3-compatible service.
type S3Config struct {
	Endpoint  string // https://s3.us-east-1.amazonaws.com, or a MinIO/R2 endpoint
	Region    string // us-east-1 by default
	Bucket    string
	Prefix    string // optional key prefix inside the bucket
	AccessKey string
	SecretKey string
	// PathStyle addresses as endpoint/bucket/key rather than
	// bucket.endpoint/key. MinIO and many S3-compatible services need it.
	PathStyle bool
	HTTP      *http.Client
}

// S3 is an ObjectStore backed by S3-compatible storage. It signs each request
// with AWS Signature Version 4 using only the standard library.
type S3 struct {
	cfg  S3Config
	host string
	base string // scheme://host[/bucket]
}

// NewS3 builds an S3 store from config.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://s3." + cfg.Region + ".amazonaws.com"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	s := &S3{cfg: cfg}
	if cfg.PathStyle {
		s.host = u.Host
		s.base = cfg.Endpoint + "/" + cfg.Bucket
	} else {
		s.host = cfg.Bucket + "." + u.Host
		s.base = u.Scheme + "://" + s.host
	}
	return s, nil
}

func (s *S3) key(k string) string {
	if s.cfg.Prefix == "" {
		return k
	}
	return strings.TrimRight(s.cfg.Prefix, "/") + "/" + k
}

// Put uploads data at key.
func (s *S3) Put(key string, data []byte) error {
	resp, err := s.do("PUT", "/"+s.key(key), nil, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return s.httpErr("put "+key, resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// Get downloads key.
func (s *S3) Get(key string) ([]byte, error) {
	resp, err := s.do("GET", "/"+s.key(key), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, s.httpErr("get "+key, resp)
	}
	return io.ReadAll(resp.Body)
}

// Has checks for key with a HEAD.
func (s *S3) Has(key string) (bool, error) {
	resp, err := s.do("HEAD", "/"+s.key(key), nil, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == 404 {
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		return false, s.httpErr("head "+key, resp)
	}
	return true, nil
}

type listResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	IsTruncated bool   `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
}

// List returns keys under prefix (with the store prefix applied).
func (s *S3) List(prefix string) ([]string, error) {
	full := s.key(prefix)
	var keys []string
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", full)
		if token != "" {
			q.Set("continuation-token", token)
		}
		path := "/"
		if s.cfg.PathStyle {
			path = "/" + s.cfg.Bucket
		}
		resp, err := s.do("GET", path, q, nil)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("list %s: %s: %s", prefix, resp.Status, strings.TrimSpace(string(body)))
		}
		var lr listResult
		if err := xml.Unmarshal(body, &lr); err != nil {
			return nil, err
		}
		strip := strings.TrimRight(s.cfg.Prefix, "/")
		for _, c := range lr.Contents {
			k := c.Key
			if strip != "" {
				k = strings.TrimPrefix(k, strip+"/")
			}
			keys = append(keys, k)
		}
		if !lr.IsTruncated || lr.NextToken == "" {
			break
		}
		token = lr.NextToken
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *S3) do(method, path string, query url.Values, body []byte) (*http.Response, error) {
	raw := s.base + path
	if s.cfg.PathStyle && path == "/"+s.cfg.Bucket {
		raw = s.cfg.Endpoint + path
	}
	if len(query) > 0 {
		raw += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, raw, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	s.sign(req, path, query, body)
	return s.cfg.HTTP.Do(req)
}

func (s *S3) httpErr(what string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(b)))
}

// sign applies AWS Signature Version 4 to req.
func (s *S3) sign(req *http.Request, canonicalPath string, query url.Values, body []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256hex(body)

	host := s.host
	if s.cfg.PathStyle {
		host = req.URL.Host
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"

	canonicalQuery := ""
	if len(query) > 0 {
		canonicalQuery = query.Encode()
	}
	canonicalURI := uriEncodePath(canonicalPath)
	canonicalRequest := req.Method + "\n" + canonicalURI + "\n" + canonicalQuery + "\n" +
		canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash

	scope := dateStamp + "/" + s.cfg.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256hex([]byte(canonicalRequest))

	kDate := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, s.cfg.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, signature))
}

func sha256hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uriEncodePath encodes each path segment per SigV4 rules, keeping slashes.
func uriEncodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = uriEncodeSegment(seg)
	}
	return strings.Join(parts, "/")
}

func uriEncodeSegment(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
