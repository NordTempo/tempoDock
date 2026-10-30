package s3

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSign reproduces the GET Object example of AWS's Signature Version 4
// documentation ("Examples: Signature Calculations in AWS Signature
// Version 4", Amazon S3 API Reference), header for header.
func TestSign(t *testing.T) {
	c := &Client{
		Bucket: Bucket{
			Endpoint: "https://s3.amazonaws.com", Name: "examplebucket", Region: "us-east-1",
			AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) },
	}
	u, err := c.url("test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "https://examplebucket.s3.amazonaws.com/test.txt" {
		t.Fatalf("url = %s", u)
	}
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	req.Header.Set("Range", "bytes=0-9")
	c.sign(req, emptyHash)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
}

func TestURL(t *testing.T) {
	c := &Client{Bucket: Bucket{Endpoint: "http://127.0.0.1:9000", Name: "dock", PathStyle: true}}
	u, err := c.url("dock-registry/docker/registry/v2/repositories/dock/p1_e2_s3/_manifests/tags/a b/current/link", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:9000/dock/dock-registry/docker/registry/v2/repositories/dock/p1_e2_s3/_manifests/tags/a%20b/current/link"
	if u.String() != want {
		t.Errorf("url =\n%s\nwant\n%s", u, want)
	}
}

func TestValidate(t *testing.T) {
	ok := Bucket{Endpoint: "https://fsn1.your-objectstorage.com", Name: "dock-images", AccessKeyID: "k", SecretAccessKey: "s"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Bucket){
		"endpoint with bucket": func(b *Bucket) { b.Endpoint = "https://fsn1.your-objectstorage.com/dock-images" },
		"no scheme":            func(b *Bucket) { b.Endpoint = "fsn1.your-objectstorage.com" },
		"upper case bucket":    func(b *Bucket) { b.Name = "Dock" },
		"no secret":            func(b *Bucket) { b.SecretAccessKey = "" },
	} {
		b := ok
		mut(&b)
		if b.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, b)
		}
	}
}

// TestList pages through a listing with continuation tokens and turns an
// error response into an *Error.
func TestList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.URL.Path != "/b/" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
			return
		}
		if r.URL.Query().Get("continuation-token") == "" {
			_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t+1</NextContinuationToken>`+
				`<Contents><Key>p/a</Key><Size>3</Size></Contents></ListBucketResult>`)
			return
		}
		_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>p/b</Key><Size>4</Size></Contents></ListBucketResult>`)
	}))
	defer srv.Close()
	c := &Client{Bucket: Bucket{Endpoint: srv.URL, Name: "b", PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s"}}
	var got []Object
	err := c.List(t.Context(), "p/", func(page []Object) error { got = append(got, page...); return nil })
	if err != nil || len(got) != 2 || got[0] != (Object{"p/a", 3}) || got[1] != (Object{"p/b", 4}) {
		t.Fatalf("List = %v, %v", got, err)
	}
	c.Bucket.Name = "other"
	err = c.Check(t.Context())
	if e, ok := errors.AsType[*Error](err); !ok || e.Code != "AccessDenied" || e.Status != http.StatusForbidden {
		t.Fatalf("Check = %v", err)
	}
	if err := c.Put(t.Context(), "big", bytes.NewReader(nil), MaxPut+1); err == nil {
		t.Fatal("Put took more than one request can")
	}
}
