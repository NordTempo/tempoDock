package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// AnalyticsAsset is Dock Analytics' store for linux/arch in a release
// (ADR 0037): servers don't get it with an update; the control plane
// downloads it when analytics runs.
func AnalyticsAsset(arch string) string { return "dock-analytics-linux-" + arch }

// AnalyticsDir holds the store's binaries the control plane downloaded,
// a directory per version.
func AnalyticsDir(dataDir string) string { return filepath.Join(dataDir, "analytics", "release") }

// StageAnalytics downloads the store's binary for arch from release
// version into AnalyticsDir, unless a copy that matches the release's
// signed SHA256SUMS is there, and makes it executable. It returns the binary's
// path and SHA-256.
func StageAnalytics(ctx context.Context, src Source, dataDir, version, arch string) (path, sum string, err error) {
	if !IsRelease(version) {
		return "", "", fmt.Errorf("dock %s is a development build, with no release to take the analytics store from; build it with `make analytics-binary` and set [analytics] store_binary", version)
	}
	dir := filepath.Join(AnalyticsDir(dataDir), version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	sums, err := ReadSums(dir, version)
	if err != nil {
		if sums, err = downloadSums(ctx, src, dir, version); err != nil {
			return "", "", err
		}
	}
	name := AnalyticsAsset(arch)
	if err := stageAsset(ctx, src, dir, version, name, sums); err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, name)
	return path, sums[name], os.Chmod(path, 0o755)
}

// PruneAnalytics removes the store's binaries of versions other than
// keep, once the store runs on keep's.
func PruneAnalytics(dataDir, keep string) error {
	if _, err := os.Stat(AnalyticsDir(dataDir)); os.IsNotExist(err) {
		return nil
	}
	return pruneReleases(AnalyticsDir(dataDir), keep)
}
