package gateway

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// listItem is a name in a listing: an object or, with a delimiter, a
// common prefix.
type listItem struct {
	name   string
	prefix bool
	info   s3.Info
}

// list answers ListObjectsV2: the spool's objects merged with the
// bucket's, in key order. Continuation tokens are the last name returned
// (base64), so a page resumes after it in both. Under an unfinished
// upload the spool knows, the bucket isn't asked: that's a push's commit
// cleaning up, and it must not wait on the bucket. Nor for a key reported
// missing during a push (pretendNowLocked).
func (g *Gateway) list(ctx context.Context, w http.ResponseWriter, q url.Values) error {
	prefix, delim := q.Get("prefix"), q.Get("delimiter")
	maxKeys := 1000
	if v, err := strconv.Atoi(q.Get("max-keys")); err == nil && v >= 0 && v < maxKeys {
		maxKeys = v
	}
	after := q.Get("start-after")
	if t := q.Get("continuation-token"); t != "" {
		b, err := base64.RawURLEncoding.DecodeString(t)
		if err != nil {
			return &s3Error{http.StatusBadRequest, "InvalidArgument", "bad continuation token"}
		}
		after = string(b)
	}

	g.mu.Lock()
	var local []listItem
	for _, k := range g.sp.localKeys(prefix) {
		e := g.sp.idx[k]
		it := item(prefix, delim, s3.Info{Key: k, Size: e.size, ETag: e.etag, LastModified: e.mod})
		if it.name <= after || g.pretendLocked(k) {
			continue
		}
		local = append(local, it)
	}
	onlyLocal := isLocal(prefix) && len(g.sp.localKeys(prefix)) > 0
	// Distribution lists a key it was just told is missing (its Stat
	// falls back to a listing): the same answer, without the bucket.
	onlyLocal = onlyLocal || g.pretendNowLocked(prefix)
	g.mu.Unlock()
	local = dedupe(local)

	var remote []listItem
	truncated, bucketLast := false, ""
	if !onlyLocal && maxKeys > 0 {
		c, err := g.client()
		if err != nil {
			return err
		}
		page, err := g.listBucket(ctx, c, s3.ListInput{Prefix: prefix, Delimiter: delim, StartAfter: startAfter(after, delim), Max: maxKeys})
		if err != nil {
			return err
		}
		for _, o := range page.Objects {
			bucketLast = max(bucketLast, o.Key)
			g.mu.Lock()
			skip := g.pretendLocked(o.Key)
			g.mu.Unlock()
			if !skip && o.Key > after {
				remote = append(remote, listItem{name: o.Key, info: o})
			}
		}
		for _, p := range page.Prefixes {
			bucketLast = max(bucketLast, p)
			if p > after {
				remote = append(remote, listItem{name: p, prefix: true})
			}
		}
		slices.SortFunc(remote, func(a, b listItem) int { return strings.Compare(a.name, b.name) })
		truncated = page.Next != ""
	}

	merged := merge(local, remote)
	if truncated {
		// Past the bucket's last name the page doesn't know what's in the
		// bucket yet: stop there.
		i, found := slices.BinarySearchFunc(merged, bucketLast, func(it listItem, n string) int { return strings.Compare(it.name, n) })
		if found {
			i++
		}
		merged = merged[:i]
	}
	more := truncated
	if len(merged) > maxKeys {
		merged, more = merged[:maxKeys], true
	}
	type content struct {
		Key          string
		LastModified string
		ETag         string
		Size         int64
		StorageClass string
	}
	out := struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Name                  string
		Prefix                string
		Delimiter             string `xml:",omitempty"`
		MaxKeys               int
		KeyCount              int
		IsTruncated           bool
		ContinuationToken     string `xml:",omitempty"`
		NextContinuationToken string `xml:",omitempty"`
		StartAfter            string `xml:",omitempty"`
		Contents              []content
		CommonPrefixes        []struct{ Prefix string }
	}{Name: "dock", Prefix: prefix, Delimiter: delim, MaxKeys: maxKeys, KeyCount: len(merged), IsTruncated: more,
		ContinuationToken: q.Get("continuation-token"), StartAfter: q.Get("start-after")}
	for _, it := range merged {
		if it.prefix {
			out.CommonPrefixes = append(out.CommonPrefixes, struct{ Prefix string }{it.name})
			continue
		}
		out.Contents = append(out.Contents, content{Key: it.name, LastModified: it.info.LastModified.UTC().Format(time.RFC3339Nano),
			ETag: it.info.ETag, Size: it.info.Size, StorageClass: "STANDARD"})
	}
	if more {
		last := bucketLast // a page the spool and the stuck blobs emptied
		if len(merged) > 0 {
			last = merged[len(merged)-1].name
		}
		out.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(last))
	}
	return writeXML(w, out)
}

// listBucket is one listing request with MetaTimeout per attempt and
// MetaAttempts attempts.
func (g *Gateway) listBucket(ctx context.Context, c *s3.Client, in s3.ListInput) (s3.ListPage, error) {
	var page s3.ListPage
	err := g.retryMeta(ctx, func(ctx context.Context) (err error) {
		page, err = c.ListPage(ctx, in)
		return err
	})
	return page, err
}

// retryMeta runs a listing or delete with MetaTimeout per attempt,
// retrying transient failures.
func (g *Gateway) retryMeta(ctx context.Context, do func(context.Context) error) error {
	var err error
	for attempt := range g.t.MetaAttempts {
		if attempt > 0 && !sleep(ctx, time.Duration(attempt)*500*time.Millisecond) {
			break
		}
		actx, cancel := context.WithTimeout(ctx, g.t.MetaTimeout)
		start := g.now()
		err = do(actx)
		timedOut := actx.Err() == context.DeadlineExceeded
		cancel()
		if ctx.Err() == nil {
			g.record(start, err, timedOut)
		}
		if err == nil || !s3.IsTransient(err) || ctx.Err() != nil {
			break
		}
	}
	return ctxErr(ctx, err)
}

// item is key as a listing with prefix and delim shows it: the key, or
// the common prefix it falls under.
func item(prefix, delim string, info s3.Info) listItem {
	if delim != "" {
		if i := strings.Index(info.Key[len(prefix):], delim); i >= 0 {
			return listItem{name: info.Key[:len(prefix)+i+len(delim)], prefix: true}
		}
	}
	return listItem{name: info.Key, info: info}
}

// dedupe drops repeated common prefixes from a sorted list.
func dedupe(items []listItem) []listItem {
	return slices.CompactFunc(items, func(a, b listItem) bool { return a.name == b.name })
}

// merge joins two sorted lists; on a name in both the spool's (a) wins.
func merge(a, b []listItem) []listItem {
	out := make([]listItem, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j == len(b) || i < len(a) && a[i].name < b[j].name:
			out = append(out, a[i])
			i++
		case i == len(a) || b[j].name < a[i].name:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}

// startAfter is what the bucket is asked to start after for a page that
// resumes after name. A common prefix must skip everything under it, or
// the bucket would return it again.
func startAfter(name, delim string) string {
	if name != "" && delim != "" && strings.HasSuffix(name, delim) {
		return name + "\U0010FFFF"
	}
	return name
}
