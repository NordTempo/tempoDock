package s3

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxSkew is how far a signed request's time may be from ours, as S3
// allows.
const maxSkew = 15 * time.Minute

// Verify checks req's AWS Signature Version 4 header signature against
// one key pair, as an S3 server would: the registry's gateway uses it to
// accept only the registry it wrote the keys for. It returns the payload
// hash the client signed (a hex SHA-256 or UNSIGNED-PAYLOAD); the caller
// checks the body against it while reading it. Presigned URLs and
// chunked (streaming) signatures are not accepted.
func Verify(req *http.Request, accessKeyID, secret string, now time.Time) (payload string, _ error) {
	auth, ok := strings.CutPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
	if !ok {
		return "", errors.New("s3: not a SigV4 header signature")
	}
	var cred, signed, sig string
	for f := range strings.SplitSeq(auth, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
		switch k {
		case "Credential":
			cred = v
		case "SignedHeaders":
			signed = v
		case "Signature":
			sig = v
		}
	}
	// Credential is <key ID>/<day>/<region>/s3/aws4_request.
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[3] != "s3" || parts[4] != "aws4_request" || sig == "" || signed == "" {
		return "", errors.New("s3: malformed authorization header")
	}
	if subtle.ConstantTimeCompare([]byte(parts[0]), []byte(accessKeyID)) != 1 {
		return "", errors.New("s3: unknown access key")
	}
	amzDate := req.Header.Get("X-Amz-Date")
	t, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil || amzDate[:8] != parts[1] {
		return "", errors.New("s3: bad or missing X-Amz-Date")
	}
	if d := now.Sub(t); d > maxSkew || d < -maxSkew {
		return "", errors.New("s3: the request time is too far from the server's")
	}
	payload = req.Header.Get("X-Amz-Content-Sha256")
	if payload == "" || strings.HasPrefix(payload, "STREAMING-") {
		return "", errors.New("s3: missing or streaming payload hash")
	}
	names := strings.Split(signed, ";")
	values := make(map[string]string, len(names))
	for _, n := range names {
		switch n {
		case "host":
			values[n] = req.Host
		case "content-length":
			values[n] = strconv.FormatInt(req.ContentLength, 10)
		default:
			values[n] = canonicalValue(req.Header.Values(n))
		}
	}
	if values["host"] == "" {
		return "", errors.New("s3: the host header must be signed")
	}
	path, _, _ := strings.Cut(req.RequestURI, "?")
	if path == "" {
		path = req.URL.EscapedPath()
	}
	_, want := signature(req.Method, path, canonicalQuery(req.URL.Query()), names, values, payload, amzDate, parts[2], secret)
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return "", errors.New("s3: signature mismatch")
	}
	return payload, nil
}

// canonicalValue is a header's value as SigV4 signs it: values joined
// with commas, trimmed, runs of spaces collapsed.
func canonicalValue(vs []string) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(out, ",")
}
