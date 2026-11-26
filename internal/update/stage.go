package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrMismatch is a file whose SHA-256 isn't the one SHA256SUMS lists.
var ErrMismatch = errors.New("doesn't match the release's SHA256SUMS")

// Sums is a parsed SHA256SUMS: asset name to hex SHA-256.
type Sums map[string]string

// ParseSums reads `sha256sum` output: "<hex>  <name>" per line.
func ParseSums(b []byte) (Sums, error) {
	s := Sums{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimSpace(name), "*")
		if raw, err := hex.DecodeString(sum); !ok || err != nil || len(raw) != sha256.Size || name == "" {
			return nil, fmt.Errorf("SHA256SUMS line %q is not \"<sha256>  <file>\"", line)
		}
		s[name] = strings.ToLower(sum)
	}
	return s, sc.Err()
}

// Check verifies the file at dir/name against the sums.
func (s Sums) Check(dir, name string) error {
	want, ok := s[name]
	if !ok {
		return fmt.Errorf("SHA256SUMS doesn't list %s", name)
	}
	got, err := HashFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s %w", name, ErrMismatch)
	}
	return nil
}

// ReadSums reads release version's SHA256SUMS in dir and checks it
// against the SigAsset next to it (VerifySums), without following
// symlinks.
func ReadSums(dir, version string) (Sums, error) {
	raw, err := readSmall(filepath.Join(dir, SumsAsset))
	if err != nil {
		return nil, err
	}
	sig, err := readSmall(filepath.Join(dir, SigAsset))
	if errors.Is(err, fs.ErrNotExist) {
		sig, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := VerifySums(version, raw, sig); err != nil {
		return nil, err
	}
	return ParseSums(raw)
}

// readSmall reads up to maxSums bytes of the file at path, without
// following a symlink.
func readSmall(path string) ([]byte, error) {
	f, err := OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSums))
}

// Assets lists what a server of each arch needs from release version:
// SHA256SUMS first, then the install tarball and a binary per arch. The
// SigAsset goes with SHA256SUMS when the release has one.
func Assets(version string, archs ...string) []string {
	out := []string{SumsAsset, InstallAsset(version)}
	for _, a := range archs {
		out = append(out, BinaryAsset(a))
	}
	return out
}

// Stage downloads release version from src into ReleaseDir(dataDir,
// version) for servers of the given archs: SHA256SUMS and its signature
// first (kept from an earlier call for the same version), then each
// asset, hashed while it streams to disk and refused (deleted) unless it
// matches. Assets already there and matching are kept, so staging again
// for another arch only downloads its binary. Other staged versions are
// removed once this one is complete. Each arch's CaddyAsset is staged too
// when the release has one. It returns the sums.
func Stage(ctx context.Context, src Source, dataDir, version string, archs ...string) (Sums, error) {
	if !IsRelease(version) {
		return nil, fmt.Errorf("%q is not a release version", version)
	}
	dir := ReleaseDir(dataDir, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// A SHA256SUMS staged before is kept: it is this version's, and the
	// nodes' updates after a control plane restart then need no source.
	sums, err := ReadSums(dir, version)
	if err != nil {
		if sums, err = downloadSums(ctx, src, dir, version); err != nil {
			return nil, err
		}
	}
	for _, name := range Assets(version, archs...)[1:] {
		if err := stageAsset(ctx, src, dir, version, name, sums); err != nil {
			return nil, err
		}
	}
	for _, a := range archs {
		if _, ok := sums[CaddyAsset(a)]; ok {
			if err := stageAsset(ctx, src, dir, version, CaddyAsset(a), sums); err != nil {
				return nil, err
			}
		}
	}
	return sums, pruneReleases(filepath.Dir(dir), version)
}

// downloadSums downloads version's SHA256SUMS and signature into dir,
// once they verify, and parses the sums.
func downloadSums(ctx context.Context, src Source, dir, version string) (Sums, error) {
	f, err := fetchSums(ctx, src, version)
	if err != nil {
		return nil, err
	}
	if f.sig == nil {
		err = RemoveIfExists(dir, SigAsset)
	} else {
		err = os.WriteFile(filepath.Join(dir, SigAsset), f.sig, 0o600)
	}
	if err != nil {
		return nil, err
	}
	return f.sums, os.WriteFile(filepath.Join(dir, SumsAsset), f.raw, 0o600)
}

// fetchedSums is a release's SHA256SUMS, raw and parsed, and its
// signature (nil for an unsigned release).
type fetchedSums struct {
	sums     Sums
	raw, sig []byte
	signed   bool
}

// fetchSums downloads version's SHA256SUMS and SigAsset and checks them
// (VerifySums).
func fetchSums(ctx context.Context, src Source, version string) (fetchedSums, error) {
	var f fetchedSums
	var err error
	if f.raw, err = fetchSmall(ctx, src, version, SumsAsset); err != nil {
		return f, err
	}
	f.sig, err = fetchSmall(ctx, src, version, SigAsset)
	if errors.Is(err, ErrNoAsset) {
		f.sig, err = nil, nil
	}
	if err != nil {
		return f, err
	}
	if f.signed, err = VerifySums(version, f.raw, f.sig); err != nil {
		return f, err
	}
	f.sums, err = ParseSums(f.raw)
	return f, err
}

// fetchSmall downloads one of version's small assets, up to maxSums bytes.
func fetchSmall(ctx context.Context, src Source, version, asset string) ([]byte, error) {
	rc, err := src.Open(ctx, version, asset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxSums))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	return b, nil
}

// stageAsset downloads one asset unless a matching copy is there.
func stageAsset(ctx context.Context, src Source, dir, version, name string, sums Sums) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("release %s's SHA256SUMS doesn't list %s", version, name)
	}
	dst := filepath.Join(dir, name)
	if got, err := HashFile(dst); err == nil && got == want {
		return nil
	}
	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	err = copyVerified(ctx, src, version, name, want, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Fetch streams asset of release version from src into w, checked against
// the release's signed SHA256SUMS: how `dock self-update` downloads the
// CLI. signed is false for a release before FirstSignedRelease, checked
// against its SHA256SUMS alone. The bytes are written before the check
// ends, so on an error (ErrMismatch among them) w holds something to
// throw away.
func Fetch(ctx context.Context, src Source, version, asset string, w io.Writer) (signed bool, err error) {
	if !IsRelease(version) {
		return false, fmt.Errorf("%q is not a release version", version)
	}
	f, err := fetchSums(ctx, src, version)
	if err != nil {
		return false, err
	}
	want, ok := f.sums[asset]
	if !ok {
		return false, fmt.Errorf("release %s's SHA256SUMS doesn't list %s", version, asset)
	}
	return f.signed, copyVerified(ctx, src, version, asset, want, w)
}

// copyVerified streams version's asset name from src into w, hashing it on
// the way, and fails when it is over MaxAsset or its SHA-256 isn't want.
func copyVerified(ctx context.Context, src Source, version, name, want string, w io.Writer) error {
	rc, err := src.Open(ctx, version, name)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(rc, MaxAsset+1))
	switch {
	case err != nil:
		return fmt.Errorf("download %s: %w", name, err)
	case n > MaxAsset:
		return fmt.Errorf("%s is over %d MB; refusing it", name, MaxAsset>>20)
	case hex.EncodeToString(h.Sum(nil)) != want:
		return fmt.Errorf("%s %w; refusing it", name, ErrMismatch)
	}
	return nil
}

// pruneReleases removes staged versions other than keep.
func pruneReleases(releases, keep string) error {
	entries, err := os.ReadDir(releases)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != keep {
			if err := os.RemoveAll(filepath.Join(releases, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// File is one file of a release sent to a node over its link.
type File struct {
	Name   string
	Size   int64
	SHA256 string
}

// Receive writes a release that arrives as files back to back in body
// into ReleaseDir(dataDir, version), checking each one's size and
// SHA-256 as it streams to disk: a node getting its control plane's
// release (ADR 0028). The files must be Assets(version, arch), in that
// order, with the SigAsset after SHA256SUMS when the release has one
// (the updater decides whether it needs one). Other staged versions are
// removed.
func Receive(dataDir, version, arch string, files []File, body io.Reader) error {
	if !IsRelease(version) {
		return fmt.Errorf("%q is not a release version", version)
	}
	want := Assets(version, arch)
	if len(files) > 1 && files[1].Name == SigAsset {
		want = slices.Insert(want, 1, SigAsset)
	}
	// Dock's Caddy comes last, when the release has one.
	if len(files) == len(want)+1 && files[len(want)].Name == CaddyAsset(arch) {
		want = append(want, CaddyAsset(arch))
	}
	if len(files) != len(want) {
		return fmt.Errorf("an update for linux/%s needs %d files, got %d", arch, len(want), len(files))
	}
	dir := ReleaseDir(dataDir, version)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for i, f := range files {
		if f.Name != want[i] || f.Size < 0 || f.Size > MaxAsset {
			return fmt.Errorf("unexpected file %q (%d bytes) in the update; want %s", f.Name, f.Size, want[i])
		}
		if err := receiveFile(filepath.Join(dir, f.Name), f, io.LimitReader(body, f.Size)); err != nil {
			return err
		}
	}
	return pruneReleases(filepath.Dir(dir), version)
}

func receiveFile(dst string, f File, r io.Reader) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("receive %s: %w", f.Name, err)
	case n != f.Size:
		return fmt.Errorf("receive %s: got %d of %d bytes", f.Name, n, f.Size)
	case hex.EncodeToString(h.Sum(nil)) != f.SHA256:
		return fmt.Errorf("%s doesn't match the SHA-256 its control plane sent; refusing it", f.Name)
	}
	return nil
}
