package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The web UI deploys a folder by posting the tar.gz it packed with the
// session cookie, as fetch or XMLHttpRequest send it from the dashboard's
// own origin. A page on another origin can't.
func TestSourceUploadFromBrowserSession(t *testing.T) {
	f := newFixture(t)
	cookie := f.login()
	self := strings.TrimPrefix(f.srv.URL, "http://")

	var archive bytes.Buffer
	zw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(zw)
	body := []byte("FROM scratch\n")
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = zw.Close()

	upload := func(service string, headers map[string]string) (int, []byte) {
		req, _ := http.NewRequest("POST", f.srv.URL+shopProd+"/services/"+service+"/releases/source", bytes.NewReader(archive.Bytes()))
		req.AddCookie(cookie)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	status, b := upload("worker", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example", "Content-Type": "application/gzip"})
	if status != http.StatusForbidden {
		t.Fatalf("cross-site upload: %d %s", status, b)
	}
	status, b = upload("web", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://" + self, "Content-Type": "application/gzip"})
	var rel apitypes.Release
	if err := json.Unmarshal(b, &rel); status != http.StatusAccepted || err != nil || rel.Source != apitypes.SourceUpload {
		t.Fatalf("same-origin upload: %d %s", status, b)
	}
}
