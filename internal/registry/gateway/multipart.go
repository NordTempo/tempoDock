package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Multipart uploads are how Distribution's driver writes a layer as it
// arrives: one part per 10 MB chunk, resumed across a push's requests by
// listing the uploads and their parts. They live in the spool's mp/
// directory and never reach the bucket; completing one stores the joined
// object like a PUT.

// mpUpload is a multipart upload in the spool.
type mpUpload struct {
	key     string
	created time.Time
	parts   map[int]mpPart
}

// mpPart is an uploaded part.
type mpPart struct {
	size int64
	etag string
	mod  time.Time
}

// partPath is part n's file.
func (g *Gateway) partPath(id string, n int) string {
	return filepath.Join(g.sp.dir, "mp", id, strconv.Itoa(n))
}

// createUpload answers CreateMultipartUpload.
func (g *Gateway) createUpload(w http.ResponseWriter, key string) error {
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Join(g.sp.dir, "mp", id), 0o700); err != nil {
		return err
	}
	g.mu.Lock()
	g.sp.uploads[id] = &mpUpload{key: key, created: g.now(), parts: map[int]mpPart{}}
	g.mu.Unlock()
	return writeXML(w, struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Bucket   string
		Key      string
		UploadID string `xml:"UploadId"`
	}{Bucket: "dock", Key: key, UploadID: id})
}

// upload is id's upload of key, or NoSuchUpload.
func (g *Gateway) mpLookup(id, key string) (*mpUpload, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	u := g.sp.uploads[id]
	if u == nil || u.key != key {
		return nil, &s3Error{http.StatusNotFound, "NoSuchUpload", "The specified upload does not exist."}
	}
	return u, nil
}

// partNumber reads partNumber (1 to 10000).
func partNumber(q url.Values) (int, error) {
	n, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil || n < 1 || n > 10000 {
		return 0, &s3Error{http.StatusBadRequest, "InvalidArgument", "bad part number"}
	}
	return n, nil
}

// uploadPart answers UploadPart.
func (g *Gateway) uploadPart(w http.ResponseWriter, r *http.Request, key string, q url.Values) error {
	n, err := partNumber(q)
	if err != nil {
		return err
	}
	id := q.Get("uploadId")
	if _, err := g.mpLookup(id, key); err != nil {
		return err
	}
	if r.ContentLength < 0 {
		return &s3Error{http.StatusLengthRequired, "MissingContentLength", "Content-Length is required"}
	}
	body, _ := r.Body.(*checkedBody)
	etag, err := g.storePart(id, key, n, r.ContentLength, func(f *os.File) error {
		c, err := io.Copy(f, r.Body)
		if err == nil && c != r.ContentLength {
			err = &s3Error{http.StatusBadRequest, "IncompleteBody", "the body is shorter than its Content-Length"}
		}
		return err
	}, func() string {
		if body != nil {
			return body.sum()
		}
		return ""
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	return nil
}

// uploadPartCopy answers UploadPartCopy: a range of an object (in the
// spool or the bucket) as part n.
func (g *Gateway) uploadPartCopy(ctx context.Context, w http.ResponseWriter, r *http.Request, key string, q url.Values) error {
	n, err := partNumber(q)
	if err != nil {
		return err
	}
	id := q.Get("uploadId")
	if _, err := g.mpLookup(id, key); err != nil {
		return err
	}
	src, err := copySource(r.Header.Get("X-Amz-Copy-Source"))
	if err != nil {
		return err
	}
	rng := r.Header.Get("X-Amz-Copy-Source-Range")
	// The range's bytes land in the spool first (a copy or a download),
	// then become the part.
	tmpKey := "gateway/_uploads/" + id + "-" + strconv.Itoa(n) // local: never queued
	g.mu.Lock()
	e := g.sp.idx[src]
	var path string
	var size int64
	if e != nil {
		path, size = g.sp.path(src, e.local), e.size
	}
	g.mu.Unlock()
	var etag string
	if e != nil {
		from, to, _, err := parseRange(rng, size)
		if err != nil {
			return err
		}
		etag, err = g.storePart(id, key, n, to-from, func(f *os.File) error {
			sf, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = sf.Close() }()
			_, err = io.Copy(f, io.NewSectionReader(sf, from, to-from))
			return err
		}, func() string { return "" })
		if err != nil {
			return err
		}
	} else {
		c, err := g.client()
		if err != nil {
			return err
		}
		if _, err := g.fetch(ctx, c, src, tmpKey, rng); err != nil {
			return err
		}
		etag, err = g.adoptPart(id, key, n, tmpKey)
		if err != nil {
			return err
		}
	}
	return writeXML(w, struct {
		XMLName      xml.Name `xml:"CopyPartResult"`
		ETag         string
		LastModified string
	}{ETag: etag, LastModified: g.now().UTC().Format(time.RFC3339Nano)})
}

// storePart writes part n of upload id (size bytes from fill) and
// returns its ETag (sum's, else one from its size and time).
func (g *Gateway) storePart(id, key string, n int, size int64, fill func(*os.File) error, sum func() string) (string, error) {
	g.mu.Lock()
	err := g.sp.reserveLocked(size, g.now())
	g.mu.Unlock()
	g.notify()
	if err != nil {
		return "", err
	}
	f, err := g.sp.incoming()
	if err != nil {
		g.release(size)
		return "", err
	}
	err = fill(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	etag := sum()
	if etag == "" {
		etag = weakETag(size, g.now())
	}
	if err == nil {
		err = g.placePart(id, key, n, f.Name(), size, etag)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		g.release(size)
		return "", err
	}
	return etag, nil
}

// adoptPart makes the spool object tmpKey part n of upload id.
func (g *Gateway) adoptPart(id, key string, n int, tmpKey string) (string, error) {
	g.mu.Lock()
	e := g.sp.idx[tmpKey]
	if e == nil {
		g.mu.Unlock()
		return "", errNoSuchKey
	}
	delete(g.sp.idx, tmpKey) // its bytes stay counted, as the part's
	path, size, etag := g.sp.path(tmpKey, e.local), e.size, e.etag
	g.mu.Unlock()
	if err := g.placePart(id, key, n, path, size, etag); err != nil {
		_ = os.Remove(path)
		g.release(size)
		return "", err
	}
	return etag, nil
}

// placePart renames file into part n of upload id, replacing (and
// uncounting) an earlier part n.
func (g *Gateway) placePart(id, key string, n int, file string, size int64, etag string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	u := g.sp.uploads[id]
	if u == nil || u.key != key {
		return &s3Error{http.StatusNotFound, "NoSuchUpload", "The upload was aborted."}
	}
	if err := os.Rename(file, g.partPath(id, n)); err != nil {
		return err
	}
	if old, ok := u.parts[n]; ok {
		g.sp.used -= old.size
	}
	u.parts[n] = mpPart{size: size, etag: etag, mod: g.now()}
	return nil
}

// listUploads answers ListMultipartUploads for a prefix.
func (g *Gateway) listUploads(w http.ResponseWriter, prefix string) error {
	type upload struct {
		Key       string
		UploadID  string `xml:"UploadId"`
		Initiated string
	}
	out := struct {
		XMLName     xml.Name `xml:"ListMultipartUploadsResult"`
		Bucket      string
		Prefix      string
		IsTruncated bool
		Uploads     []upload `xml:"Upload"`
	}{Bucket: "dock", Prefix: prefix}
	g.mu.Lock()
	for id, u := range g.sp.uploads {
		if strings.HasPrefix(u.key, prefix) {
			out.Uploads = append(out.Uploads, upload{u.key, id, u.created.UTC().Format(time.RFC3339Nano)})
		}
	}
	g.mu.Unlock()
	slices.SortFunc(out.Uploads, func(a, b upload) int { return strings.Compare(a.Key+a.UploadID, b.Key+b.UploadID) })
	return writeXML(w, out)
}

// listParts answers ListParts, every part in one page.
func (g *Gateway) listParts(w http.ResponseWriter, key, id string) error {
	u, err := g.mpLookup(id, key)
	if err != nil {
		return err
	}
	type part struct {
		PartNumber   int
		LastModified string
		ETag         string
		Size         int64
	}
	out := struct {
		XMLName     xml.Name `xml:"ListPartsResult"`
		Bucket      string
		Key         string
		UploadID    string `xml:"UploadId"`
		IsTruncated bool
		Parts       []part `xml:"Part"`
	}{Bucket: "dock", Key: key, UploadID: id}
	g.mu.Lock()
	for n, p := range u.parts {
		out.Parts = append(out.Parts, part{n, p.mod.UTC().Format(time.RFC3339Nano), p.etag, p.size})
	}
	g.mu.Unlock()
	slices.SortFunc(out.Parts, func(a, b part) int { return a.PartNumber - b.PartNumber })
	return writeXML(w, out)
}

// completeUpload answers CompleteMultipartUpload: the listed parts,
// joined in order, become key.
func (g *Gateway) completeUpload(w http.ResponseWriter, r *http.Request, key, id string) error {
	var in struct {
		Parts []struct {
			PartNumber int
			ETag       string
		} `xml:"Part"`
	}
	if err := xml.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in); err != nil {
		return &s3Error{http.StatusBadRequest, "MalformedXML", err.Error()}
	}
	u, err := g.mpLookup(id, key)
	if err != nil {
		return err
	}
	g.mu.Lock()
	var size int64
	for i, p := range in.Parts {
		mp, ok := u.parts[p.PartNumber]
		if !ok || mp.etag != p.ETag || i > 0 && p.PartNumber <= in.Parts[i-1].PartNumber {
			g.mu.Unlock()
			return &s3Error{http.StatusBadRequest, "InvalidPart", "a listed part is missing, changed or out of order"}
		}
		size += mp.size
	}
	g.mu.Unlock()
	etag, err := g.write(r.Context(), key, size, func(f *os.File) (string, error) {
		for _, p := range in.Parts {
			pf, err := os.Open(g.partPath(id, p.PartNumber))
			if err != nil {
				return "", err
			}
			_, err = io.Copy(f, pf)
			_ = pf.Close()
			if err != nil {
				return "", err
			}
		}
		return weakETag(size, time.Now()), nil
	})
	if err != nil {
		return err
	}
	g.dropUpload(id)
	return writeXML(w, struct {
		XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
		Bucket  string
		Key     string
		ETag    string
	}{Bucket: "dock", Key: key, ETag: etag})
}

// abortUpload answers AbortMultipartUpload. The id names a directory, so
// only one this gateway could have made is acted on (uploadID); any other
// has nothing to abort.
func (g *Gateway) abortUpload(w http.ResponseWriter, id string) error {
	if uploadID(id) {
		g.dropUpload(id)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// dropUpload removes upload id and its parts.
func (g *Gateway) dropUpload(id string) {
	g.mu.Lock()
	if u := g.sp.uploads[id]; u != nil {
		for _, p := range u.parts {
			g.sp.used -= p.size
		}
		delete(g.sp.uploads, id)
	}
	g.mu.Unlock()
	_ = os.RemoveAll(filepath.Join(g.sp.dir, "mp", id))
}

// uploadID reports whether id has the shape createUpload gives upload
// ids: 32 lower-case hex digits, nothing a path could be made of.
func uploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range []byte(id) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
