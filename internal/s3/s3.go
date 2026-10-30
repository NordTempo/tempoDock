// Package s3 is a small client for S3-compatible object storage (ADR
// 0034): the few requests Dock makes itself, signed with AWS Signature
// Version 4. It lists, reads, writes and deletes the platform registry's
// objects (its size in the console, migrations) and checks a storage
// connection when it is saved. The registry's own reads and writes go
// through Distribution's driver, never through here.
//
// Any S3-compatible store works: AWS, Hetzner Object Storage, Cloudflare
// R2, Backblaze B2, MinIO, Garage. A Bucket names the endpoint, region,
// bucket and keys, and whether the bucket is addressed in the path
// (https://endpoint/bucket/key) or the host (https://bucket.endpoint/key).
package s3

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // DeleteObjects requires Content-MD5; it is not used for security.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultRegion signs requests when a Bucket has no region. Most
// S3-compatible stores accept it whatever their own region names are.
const DefaultRegion = "us-east-1"

// MaxPut is the largest object Put sends in one request (S3's limit for a
// single PUT).
const MaxPut = 5 << 30

// Bucket is one bucket in an S3-compatible store and the keys to reach it.
type Bucket struct {
	// Endpoint is the store's base URL, https://fsn1.your-objectstorage.com.
	Endpoint string
	// Region signs requests; "" is DefaultRegion.
	Region string
	// Name is the bucket's name.
	Name string
	// PathStyle puts the bucket in the URL's path instead of its host.
	PathStyle                    bool
	AccessKeyID, SecretAccessKey string
}

// bucketName is what S3 accepts as a bucket name.
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Validate checks b's shape (not whether the bucket can be reached; Check
// does that).
func (b Bucket) Validate() error {
	u, err := url.Parse(b.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return errors.New("the endpoint must be the store's base URL, like https://fsn1.your-objectstorage.com (no bucket or path)")
	}
	if !bucketName.MatchString(b.Name) {
		return fmt.Errorf("%q is not a bucket name: 3 to 63 lowercase letters, digits, dots and hyphens", b.Name)
	}
	if b.AccessKeyID == "" || b.SecretAccessKey == "" {
		return errors.New("an access key ID and a secret access key are required")
	}
	if strings.ContainsAny(b.Region, " \t\n/") {
		return fmt.Errorf("%q is not a region", b.Region)
	}
	return nil
}

// Client sends requests for one bucket.
type Client struct {
	Bucket Bucket
	// HTTP is the client to use; nil means defaultHTTP.
	HTTP *http.Client
	// now is the signing time; nil means time.Now.
	now func() time.Time
}

// defaultHTTP bounds connecting and waiting for an answer, so a store
// that stops responding fails the request instead of hanging the
// migration or the prune waiting on it. Bodies have no deadline: a large
// object takes as long as the link needs.
var defaultHTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   30 * time.Second,
	ResponseHeaderTimeout: 2 * time.Minute,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConnsPerHost:   16,
	ForceAttemptHTTP2:     true,
}}

// Object is one object in a listing.
type Object struct {
	Key  string
	Size int64
}

// Error is an error response from the store.
type Error struct {
	Status  int
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("s3: %s", http.StatusText(e.Status))
	}
	return fmt.Sprintf("s3: %s: %s (HTTP %d)", e.Code, cmp.Or(e.Message, http.StatusText(e.Status)), e.Status)
}

// IsNotFound reports whether err is a missing bucket or object.
func IsNotFound(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Status == http.StatusNotFound
}

// List calls fn with every page (up to 1000 objects) of the objects whose
// keys start with prefix, in key order.
func (c *Client) List(ctx context.Context, prefix string, fn func([]Object) error) error {
	token := ""
	for {
		page, next, err := c.list(ctx, prefix, token, 1000)
		if err != nil {
			return err
		}
		if err := fn(page); err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

// Check makes one small authenticated request to the bucket, so a wrong
// endpoint, bucket, region or key fails when a connection is saved rather
// than when the registry first needs it.
func (c *Client) Check(ctx context.Context) error {
	_, _, err := c.list(ctx, "", "", 1)
	return err
}

// HasPrefix reports whether any object's key starts with prefix.
func (c *Client) HasPrefix(ctx context.Context, prefix string) (bool, error) {
	objs, _, err := c.list(ctx, prefix, "", 1)
	return len(objs) > 0, err
}

// list is one ListObjectsV2 request; next is the continuation token, ""
// on the last page.
func (c *Client) list(ctx context.Context, prefix, token string, max int) (_ []Object, next string, _ error) {
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(max)}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	resp, err := c.do(ctx, http.MethodGet, "", q, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		IsTruncated           bool
		NextContinuationToken string
		Contents              []struct {
			Key  string
			Size int64
		}
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("s3: read the listing: %w", err)
	}
	objs := make([]Object, len(out.Contents))
	for i, o := range out.Contents {
		objs[i] = Object{Key: o.Key, Size: o.Size}
	}
	if out.IsTruncated {
		if out.NextContinuationToken == "" {
			return nil, "", errors.New("s3: a truncated listing without a continuation token")
		}
		next = out.NextContinuationToken
	}
	return objs, next, nil
}

// Get opens an object. The caller closes it.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Put writes size bytes from body to key, in one request (size up to
// MaxPut). The body is streamed, not signed.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if size > MaxPut {
		return fmt.Errorf("s3: %s is %d bytes, more than one upload takes (%d)", key, size, int64(MaxPut))
	}
	if size == 0 {
		body = http.NoBody
	}
	resp, err := c.do(ctx, http.MethodPut, key, nil, &sized{body, size}, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Delete deletes keys, up to 1000 per request. Missing keys are no error.
func (c *Client) Delete(ctx context.Context, keys []string) error {
	for chunk := range slices.Chunk(keys, 1000) {
		var b bytes.Buffer
		b.WriteString(`<Delete><Quiet>true</Quiet>`)
		for _, k := range chunk {
			b.WriteString("<Object><Key>")
			_ = xml.EscapeText(&b, []byte(k))
			b.WriteString("</Key></Object>")
		}
		b.WriteString(`</Delete>`)
		sum := md5.Sum(b.Bytes()) //nolint:gosec // see the import
		hdr := http.Header{"Content-Md5": {base64.StdEncoding.EncodeToString(sum[:])}, "Content-Type": {"application/xml"}}
		resp, err := c.do(ctx, http.MethodPost, "", url.Values{"delete": {""}}, &sized{bytes.NewReader(b.Bytes()), int64(b.Len())}, hdr)
		if err != nil {
			return err
		}
		var out struct {
			Errors []struct{ Key, Code, Message string } `xml:"Error"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out)
		_ = resp.Body.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("s3: read the delete result: %w", err)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return fmt.Errorf("s3: delete %s: %s: %s (and %d more)", e.Key, e.Code, e.Message, len(out.Errors)-1)
		}
	}
	return nil
}

// sized is a request body of a known length.
type sized struct {
	io.Reader
	n int64
}

// do signs and sends one request for key ("" is the bucket) and returns
// the response if its status is 2xx; any other status is an *Error. A
// body is streamed unsigned unless it is a *bytes.Reader.
func (c *Client) do(ctx context.Context, method, key string, query url.Values, body *sized, hdr http.Header) (*http.Response, error) {
	u, err := c.url(key, query)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = body.Reader
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	payload := emptyHash
	if body != nil {
		req.ContentLength = body.n
		payload = "UNSIGNED-PAYLOAD"
		if br, ok := body.Reader.(*bytes.Reader); ok {
			b := make([]byte, br.Len())
			_, _ = br.ReadAt(b, 0)
			payload = hexHash(b)
		}
	}
	c.sign(req, payload)
	resp, err := cmp.Or(c.HTTP, defaultHTTP).Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	e := &Error{Status: resp.StatusCode}
	_ = xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(e)
	return nil, e
}

// url is key's URL in the bucket, path style or virtual-hosted.
func (c *Client) url(key string, query url.Values) (*url.URL, error) {
	u, err := url.Parse(c.Bucket.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3: endpoint: %w", err)
	}
	p := "/" + key
	if c.Bucket.PathStyle {
		p = "/" + c.Bucket.Name + p
	} else {
		u.Host = c.Bucket.Name + "." + u.Host
	}
	u.Path, u.RawPath = p, escape(p, true)
	u.RawQuery = canonicalQuery(query)
	return u, nil
}

// emptyHash is the SHA-256 of nothing, the payload hash of a request
// without a body.
const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sign adds AWS Signature Version 4 headers to req for payload (a hex
// SHA-256 or UNSIGNED-PAYLOAD). It signs the host, the x-amz-* headers
// and any Content-MD5, Content-Type and Range.
func (c *Client) sign(req *http.Request, payload string) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	t := now().UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)

	names := []string{"host"}
	values := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-md5" || lk == "content-type" || lk == "range" {
			names = append(names, lk)
			values[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	slices.Sort(names)
	var canon strings.Builder
	for _, n := range names {
		canon.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	request := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canon.String(), signed, payload,
	}, "\n")
	region := cmp.Or(c.Bucket.Region, DefaultRegion)
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexHash([]byte(request))
	key := []byte("AWS4" + c.Bucket.SecretAccessKey)
	for _, part := range []string{day, region, "s3", "aws4_request"} {
		key = hmacSum(key, part)
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.Bucket.AccessKeyID, scope, signed, hex.EncodeToString(hmacSum(key, toSign))))
}

// hmacSum is HMAC-SHA256 of data with key, a step of SigV4's key chain.
func hmacSum(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// hexHash is the hex SHA-256 of b, as SigV4 writes hashes.
func hexHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// canonicalQuery encodes q sorted by key, as SigV4 wants it (and as the
// request is sent): spaces as %20, never +.
func canonicalQuery(q url.Values) string {
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range q {
			if !yield(k) {
				return
			}
		}
	})
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, escape(k, false)+"="+escape(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// escape percent-encodes everything but RFC 3986's unreserved characters
// (and slashes, if keepSlash).
func escape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case 'A' <= ch && ch <= 'Z', 'a' <= ch && ch <= 'z', '0' <= ch && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && keepSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
