package build

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/railwayapp/railpack/core/mise"
)

// miseAsset is one platform's mise release: the platform as the release
// names it, the SHA-256 of its .tar.gz and the SHA-256 of the mise binary
// inside (the release publishes the binary on its own too, so both come
// from the release's SHASUMS256.txt).
type miseAsset struct {
	platform string
	archive  string
	binary   string
}

// miseDigests holds what a mise release must hash to, by mise version and
// then by GOOS/GOARCH. Railpack's planner runs mise on the build host and
// would download it unchecked; Dock places it first (ensureMise) and only
// a binary matching this table is used. A Railpack upgrade that pins
// another mise fails TestMiseDigestsCoverRailpack until its row is here:
// take the digests from
// https://github.com/jdx/mise/releases/download/v<version>/SHASUMS256.txt
// and check them against a download of each archive.
var miseDigests = map[string]map[string]miseAsset{
	"2026.10.14": {
		"linux/amd64": {
			platform: "linux-x64-musl",
			archive:  "0f95d528ad0b8c54f7022be2ff22a36be149123ff9b27bdaa216d349a9f59c17",
			binary:   "b0e388c8014f9ac14a5ddf7a4042b0db28043f7f9ce485ffc2521bff02247f8e",
		},
		"linux/arm64": {
			platform: "linux-arm64-musl",
			archive:  "8f7fa64c3d08ba515aac501f18f6f43c00c381793185380fd1a92c411c00cdde",
			binary:   "c71eec4d0015f6ced74ac9d5cd755b56674df04e899d0d1566d533c95511ae6c",
		},
		"darwin/amd64": {
			platform: "macos-x64",
			archive:  "09b1d408a41cbe9ffa4bc5a0774b681c2a833919bff6138e85cd8f5e86617b07",
			binary:   "158cd8124bc1108efa6588679b87031c970f592230b618e11c409b55c58e7b27",
		},
		"darwin/arm64": {
			platform: "macos-arm64",
			archive:  "eb68c824e16af90f61e70e0d785179f335b1b95466606a5e0d8b9da504bb7bb2",
			binary:   "005b32b9c22ca9cb63ff9d35c74b3abe69c433e9ca655fea288ba3dcbe7e4918",
		},
	},
}

// miseReleases is where mise's release assets are downloaded from.
const miseReleases = "https://github.com/jdx/mise/releases/download"

// miseTimeout bounds one download of mise (about 55 MB), on top of the
// build's own context.
const miseTimeout = 5 * time.Minute

// Caps on what a download may make this process read and write, well above
// a real release (55 MB packed, 150 MB unpacked).
const (
	miseMaxArchive = 256 << 20
	miseMaxBinary  = 512 << 20
)

// miseInArchive is the binary's path inside a release archive.
const miseInArchive = "mise/bin/mise"

var errMiseTimeout = errors.New("mise download timeout")

// miseLock lets one build of this process place mise at a time, so
// builds starting together share one download. A channel rather than a
// mutex so a waiting build can still be cancelled.
var miseLock = make(chan struct{}, 1)

// miseVersion is the mise version the compiled-in Railpack runs.
func miseVersion() string { return strings.TrimSpace(mise.Version) }

// ensureMise makes sure the mise binary Railpack's planner runs is in
// Railpack's install directory and matches miseDigests, downloading it
// when it isn't. Railpack uses a binary it finds there as it is and
// downloads one only when none is there, so this runs before every plan.
// It costs two file reads once the binary is in place. A line goes to
// logs when a download starts.
func ensureMise(ctx context.Context, logs io.Writer) error {
	version, platform := miseVersion(), runtime.GOOS+"/"+runtime.GOARCH
	asset, ok := miseDigests[version][platform]
	if !ok {
		return fmt.Errorf("this Dock has no checksum for mise %s on %s, which Railpack builds need: add a Dockerfile to build from, or build on a linux amd64 or arm64 server", version, platform)
	}
	return placeMise(ctx, mise.InstallDir, miseReleases, version, asset, logs)
}

// placeMise puts the mise binary of version at dir/mise-<version>, the
// path Railpack looks at, unless a checked one is there already. A binary
// counts as checked when the marker beside it (miseMarker) names its
// digest, size and modification time; a binary without one (left by an
// older Dock, where Railpack downloaded it) is hashed once and kept when
// it matches. Anything else is replaced by a download from base, which is
// placed only when both the archive and the binary in it match asset.
func placeMise(ctx context.Context, dir, base, version string, asset miseAsset, logs io.Writer) error {
	select {
	case miseLock <- struct{}{}:
		defer func() { <-miseLock }()
	case <-ctx.Done():
		return context.Cause(ctx)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create mise's directory: %w", err)
	}
	// The path is fixed and under /tmp: another user who made it first
	// could swap the binary after the check.
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := ownDir(d); err != nil {
			return err
		}
	}

	bin := filepath.Join(dir, "mise-"+version)
	if miseChecked(bin, asset) {
		return nil
	}
	if sum, err := hashFile(bin); err == nil && sum == asset.binary {
		return writeMiseMarker(bin, asset)
	}

	_, _ = fmt.Fprintf(logs, "railpack: downloading mise %s\n", version)
	if err := downloadMise(ctx, bin, base, version, asset); err != nil {
		return err
	}
	return writeMiseMarker(bin, asset)
}

// ownDir checks that dir is a real directory of this process's user and
// takes away group and world write access if it has any.
func ownDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check mise's directory: %w", err)
	}
	if !fi.IsDir() || !ownedByUs(fi) {
		return fmt.Errorf("%s, where Railpack keeps mise, is not a directory owned by the user Dock runs as: remove it and retry the build", dir)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		if err := os.Chmod(dir, 0o755); err != nil {
			return fmt.Errorf("check mise's directory: %w", err)
		}
	}
	return nil
}

// miseMarker is the content of the file beside a placed binary
// (<binary>.sha256): the digest it was checked against, and the size and
// modification time it had then. A binary written since no longer matches
// its marker and is checked again.
func miseMarker(asset miseAsset, fi os.FileInfo) string {
	return fmt.Sprintf("%s %d %d\n", asset.binary, fi.Size(), fi.ModTime().UnixNano())
}

// miseChecked reports whether bin is a regular file whose marker says it
// matched asset as it is now.
func miseChecked(bin string, asset miseAsset) bool {
	fi, err := os.Lstat(bin)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	marker, err := os.ReadFile(bin + ".sha256")
	return err == nil && string(marker) == miseMarker(asset, fi)
}

// writeMiseMarker records that bin matched asset, through a temporary
// file so a concurrent reader never sees half of it.
func writeMiseMarker(bin string, asset miseAsset) error {
	fi, err := os.Lstat(bin)
	if err != nil {
		return fmt.Errorf("record mise's checksum: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(bin), "mise-dock-*")
	if err != nil {
		return fmt.Errorf("record mise's checksum: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone already once renamed
	_, err = tmp.WriteString(miseMarker(asset, fi))
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), bin+".sha256")
	}
	if err != nil {
		return fmt.Errorf("record mise's checksum: %w", err)
	}
	return nil
}

// hashFile is the hex SHA-256 of a regular file, read as a stream.
func hashFile(path string) (string, error) {
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return "", errors.Join(err, errors.ErrUnsupported)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hexSum(h), nil
}

// hexSum is a hash's digest in hex.
func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// downloadMise streams the release archive for asset from base, unpacks
// the binary into a temporary file beside bin while hashing both, and
// renames it to bin only when the archive and the binary match asset. On
// any failure nothing is left in bin's directory.
func downloadMise(ctx context.Context, bin, base, version string, asset miseAsset) error {
	ctx, cancel := context.WithTimeoutCause(ctx, miseTimeout, errMiseTimeout)
	defer cancel()
	url := fmt.Sprintf("%s/v%s/mise-v%s-%s.tar.gz", base, version, version, asset.platform)
	// failed wraps a transport or disk error with what to do about it.
	failed := func(err error) error {
		if errors.Is(context.Cause(ctx), errMiseTimeout) {
			err = fmt.Errorf("it took longer than %s", miseTimeout)
		}
		return fmt.Errorf("download mise %s for the Railpack build from %s: %w; check that this server can reach it and retry, or add a Dockerfile to build from", version, url, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return failed(err)
	}
	resp, err := http.DefaultClient.Do(req) // bounded by ctx
	if err != nil {
		return failed(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return failed(fmt.Errorf("the server answered %s", resp.Status))
	}

	tmp, err := os.CreateTemp(filepath.Dir(bin), "mise-dock-*")
	if err != nil {
		return failed(err)
	}
	defer func() { // the file is gone already once renamed
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	archiveSum, binarySum := sha256.New(), sha256.New()
	body := io.TeeReader(io.LimitReader(resp.Body, miseMaxArchive), archiveSum)
	unpackErr := unpackMise(body, io.MultiWriter(tmp, binarySum))
	// The archive's digest covers all of it, also what follows the binary.
	if _, err := io.Copy(io.Discard, body); err != nil {
		return failed(err)
	}
	mismatch := func(what, got, want string) error {
		return fmt.Errorf("%s from %s has SHA-256 %s, not the %s this Dock expects, so it was not used: retry the build, and if it happens again add a Dockerfile to build from and report it, as the download is being altered or the release has changed", what, url, got, want)
	}
	if got := hexSum(archiveSum); got != asset.archive {
		return mismatch("the mise archive", got, asset.archive)
	}
	if unpackErr != nil {
		return failed(unpackErr)
	}
	if got := hexSum(binarySum); got != asset.binary {
		return mismatch("the mise binary in the archive", got, asset.binary)
	}

	if err := tmp.Chmod(0o755); err != nil {
		return failed(err)
	}
	if err := tmp.Close(); err != nil {
		return failed(err)
	}
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return failed(err)
	}
	return nil
}

// unpackMise copies the mise binary out of a release's .tar.gz stream
// into w, up to miseMaxBinary.
func unpackMise(archive io.Reader, w io.Writer) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("no %s in the archive", miseInArchive)
		}
		if err != nil {
			return err
		}
		if h.Name == miseInArchive && h.Typeflag == tar.TypeReg {
			_, err := io.Copy(w, io.LimitReader(tr, miseMaxBinary))
			return err
		}
	}
}
