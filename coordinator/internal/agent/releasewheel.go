package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReleaseWheelURL returns the download URL of the scimesh wheel attached to
// the GitHub release that matches the given binary version (for example
// "1.1.0-alpha.10"), plus the wheel file name. The wheel is version-locked to
// the binary so a worker's catalog always matches its task runner.
func ReleaseWheelURL(version string) (string, string, error) {
	if version == "" || version == "dev" {
		return "", "", fmt.Errorf("no release wheel for build %q", version)
	}
	filename := fmt.Sprintf("scimesh-%s-py3-none-any.whl", NormalizePEP440(version))
	return fmt.Sprintf("https://github.com/emil28092005/SciMesh/releases/download/v%s/%s", version, filename), filename, nil
}

// NormalizePEP440 turns our release tag suffixes into the PEP 440 form
// setuptools uses for wheel names: 1.1.0-alpha.10 -> 1.1.0a10,
// 1.1.0-beta.2 -> 1.1.0b2, 1.1.0-rc.1 -> 1.1.0rc1. Stable tags pass through.
func NormalizePEP440(version string) string {
	for from, to := range map[string]string{"-alpha.": "a", "-beta.": "b", "-rc.": "rc"} {
		version = strings.ReplaceAll(version, from, to)
	}
	return version
}

// DownloadWheel fetches the release wheel into dir (config directory of the
// wizard / serve data dir) and returns the local path. Best-effort download
// with a generous timeout: wheels can be several MB.
func DownloadWheel(ctx context.Context, url, dir string) (string, error) {
	target := filepath.Join(dir, wheelNameFromURL(url))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download wheel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download wheel: HTTP %d", resp.StatusCode)
	}
	//nolint:gosec // G304: target is our own config dir + a fixed wheel name
	out, err := os.Create(target)
	if err != nil {
		return "", fmt.Errorf("download wheel: %w", err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("download wheel: %w", err)
	}
	return target, nil
}

// wheelNameFromURL extracts the trailing file name of a wheel URL.
func wheelNameFromURL(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}
