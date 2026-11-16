package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The requests below serve the registry's gateway
// (internal/registry/gateway): it reads through to the bucket and uploads
// what the registry wrote to its spool.

// Info is an object's metadata, from a HEAD or a listing.
type Info struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// Head reads key's metadata. A missing key is an error IsNotFound
// reports.
func (c *Client) Head(ctx context.Context, key string) (Info, error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, nil, nil)
	if err != nil {
		return Info{}, err
	}
	_ = resp.Body.Close()
	return Info{Key: key, Size: resp.ContentLength, ETag: resp.Header.Get("ETag"), LastModified: lastModified(resp.Header)}, nil
}

// Open sends a GET for key with the Range header rng ("" for all of it)
// and returns the response with its body to stream; the caller closes it.
// Status and headers (Content-Length, Content-Range, ETag, Last-Modified)
// are the store's.
func (c *Client) Open(ctx context.Context, key, rng string) (*http.Response, error) {
	var hdr http.Header
	if rng != "" {
		hdr = http.Header{"Range": {rng}}
	}
	return c.do(ctx, http.MethodGet, key, nil, nil, hdr)
}

// ListInput is one ListObjectsV2 request.
type ListInput struct {
	Prefix, Delimiter, StartAfter, Token string
	// Max is the page size, at most 1000; 0 is 1000.
	Max int
}

// ListPage is one page of a listing: objects and, with a delimiter, the
// common prefixes ("directories"). Next is the continuation token, ""
// on the last page.
type ListPage struct {
	Objects  []Info
	Prefixes []string
	Next     string
}

// ListPage sends one ListObjectsV2 request.
func (c *Client) ListPage(ctx context.Context, in ListInput) (ListPage, error) {
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(min(cmpOr(in.Max, 1000), 1000))}}
	for k, v := range map[string]string{"prefix": in.Prefix, "delimiter": in.Delimiter, "start-after": in.StartAfter, "continuation-token": in.Token} {
		if v != "" {
			q.Set(k, v)
		}
	}
	resp, err := c.do(ctx, http.MethodGet, "", q, nil, nil)
	if err != nil {
		return ListPage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		IsTruncated           bool
		NextContinuationToken string
		Contents              []struct {
			Key          string
			Size         int64
			ETag         string
			LastModified time.Time
		}
		CommonPrefixes []struct{ Prefix string }
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return ListPage{}, fmt.Errorf("s3: read the listing: %w", err)
	}
	page := ListPage{Objects: make([]Info, len(out.Contents))}
	for i, o := range out.Contents {
		page.Objects[i] = Info{Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.LastModified}
	}
	for _, p := range out.CommonPrefixes {
		page.Prefixes = append(page.Prefixes, p.Prefix)
	}
	if out.IsTruncated {
		if out.NextContinuationToken == "" {
			return ListPage{}, errors.New("s3: a truncated listing without a continuation token")
		}
		page.Next = out.NextContinuationToken
	}
	return page, nil
}

// Part is an uploaded part of a multipart upload.
type Part struct {
	Number int
	ETag   string
}

// CreateMultipart starts a multipart upload of key and returns its ID.
func (c *Client) CreateMultipart(ctx context.Context, key string) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", fmt.Errorf("s3: read the new upload's ID: %w", err)
	}
	if out.UploadID == "" {
		return "", errors.New("s3: the store started an upload without an ID")
	}
	return out.UploadID, nil
}

// UploadPart uploads part n (from 1) of upload id: size bytes of body.
func (c *Client) UploadPart(ctx context.Context, key, id string, n int, body io.Reader, size int64) (Part, error) {
	q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {id}}
	resp, err := c.do(ctx, http.MethodPut, key, q, &sized{body, size}, nil)
	if err != nil {
		return Part{}, err
	}
	_ = resp.Body.Close()
	return Part{Number: n, ETag: resp.Header.Get("ETag")}, nil
}

// CompleteMultipart joins the parts of upload id into key.
func (c *Client) CompleteMultipart(ctx context.Context, key, id string, parts []Part) error {
	var b bytes.Buffer
	b.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>", p.Number)
		_ = xml.EscapeText(&b, []byte(p.ETag))
		b.WriteString("</ETag></Part>")
	}
	b.WriteString("</CompleteMultipartUpload>")
	resp, err := c.do(ctx, http.MethodPost, key, url.Values{"uploadId": {id}}, &sized{bytes.NewReader(b.Bytes()), int64(b.Len())}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// S3 may answer 200 and put an error in the body.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("s3: read the completed upload: %w", err)
	}
	if bytes.Contains(body, []byte("<Error>")) {
		e := &Error{Status: resp.StatusCode}
		_ = xml.Unmarshal(body, e)
		return e
	}
	return nil
}

// AbortMultipart cancels upload id and frees its parts.
func (c *Client) AbortMultipart(ctx context.Context, key, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, url.Values{"uploadId": {id}}, nil, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// lastModified parses a Last-Modified header; zero when absent.
func lastModified(h http.Header) time.Time {
	t, _ := http.ParseTime(h.Get("Last-Modified"))
	return t
}

// cmpOr is cmp.Or for ints (kept local to read like the call sites).
func cmpOr(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// IsTransient reports whether err is worth retrying: a network error, a
// timeout, or a 5xx or 429 from the store. A 4xx is the request's fault.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout
	}
	return !errors.Is(err, context.Canceled) // a deadline is the store's slowness
}
