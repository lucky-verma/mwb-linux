// Package selfupdate replaces the running mwb binary with the latest published
// GitHub release.
//
// Every downloaded artifact is checked against the SHA-256 in the release's
// checksums.txt before it is allowed anywhere near the filesystem, and the
// replacement is atomic, so an interrupted update can never leave a truncated
// binary in place.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the upstream project queried for releases.
const Repo = "lucky-verma/mwb-linux"

// maxAssetSize caps how much will be read from a download. The binary is a few
// megabytes; anything wildly larger means something is wrong and should not be
// buffered into memory.
const maxAssetSize = 128 << 20 // 128 MiB

// allowedHosts restricts where an update may be fetched from. GitHub redirects
// asset downloads to its object storage, so both hosts are needed — but an
// unexpected redirect target must not be followed.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// Release is the subset of the GitHub release API that matters here.
type Release struct {
	Version string
	Assets  map[string]string // asset name -> download URL
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func newClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if !allowedHosts[req.URL.Hostname()] {
				return fmt.Errorf("refusing redirect to unexpected host %q", req.URL.Hostname())
			}
			return nil
		},
	}
}

func get(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("refusing non-https url %q", rawURL)
	}
	if !allowedHosts[u.Hostname()] {
		return nil, fmt.Errorf("refusing to fetch from unexpected host %q", u.Hostname())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mwb-selfupdate")

	resp, err := newClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close() //nolint:errcheck
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("GitHub rate limit reached (HTTP %d) — try again later", resp.StatusCode)
		}
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, rawURL)
	}
	return resp.Body, nil
}

// LatestRelease returns the newest non-draft, non-prerelease release.
func LatestRelease(ctx context.Context, repo string) (*Release, error) {
	body, err := get(ctx, "https://api.github.com/repos/"+repo+"/releases/latest")
	if err != nil {
		return nil, err
	}
	defer body.Close() //nolint:errcheck

	var gh ghRelease
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&gh); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if gh.Draft || gh.Prerelease {
		return nil, fmt.Errorf("latest release %s is a draft or prerelease", gh.TagName)
	}

	rel := &Release{Version: gh.TagName, Assets: make(map[string]string, len(gh.Assets))}
	for _, a := range gh.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// AssetName is the release artifact for the running architecture. It must match
// the archives name_template in .goreleaser.yaml.
func AssetName() string {
	return "mwb-linux-" + runtime.GOARCH
}

// ParseVersion turns a tag such as "v0.5.1" into comparable components. The ok
// result is false for anything unparseable, including the "dev" placeholder
// used by builds made outside the release pipeline.
func ParseVersion(v string) (parts [3]int, ok bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 { // drop prerelease/build metadata
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	if len(fields) == 0 || len(fields) > 3 {
		return parts, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// IsNewer reports whether latest is a later version than current.
//
// An unparseable current version — a locally built binary reporting "dev" — is
// treated as older, so `mwb update` still does something useful there.
func IsNewer(current, latest string) bool {
	l, ok := ParseVersion(latest)
	if !ok {
		return false
	}
	c, ok := ParseVersion(current)
	if !ok {
		return true
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// ParseChecksums reads the "<sha256>  <filename>" lines goreleaser writes.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, err
	}
	sums := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("no checksums found")
	}
	return sums, nil
}

// download fetches an asset and fails unless it matches wantSHA256.
func download(ctx context.Context, rawURL, wantSHA256 string) ([]byte, error) {
	body, err := get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer body.Close() //nolint:errcheck

	data, err := io.ReadAll(io.LimitReader(body, maxAssetSize))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("downloaded asset is empty")
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != wantSHA256 {
		return nil, fmt.Errorf("checksum mismatch: expected %s, got %s", wantSHA256, got)
	}
	return data, nil
}

// replaceBinary atomically swaps the file at dest for the given bytes.
//
// The temporary file is created in dest's own directory so the final rename is
// a same-filesystem operation, which the kernel performs atomically. A crash at
// any point therefore leaves either the old binary or the new one, never a
// half-written file. Replacing a running executable this way is safe on Linux:
// the kernel keeps the old inode alive for the running process.
func replaceBinary(dest string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".mwb-update-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("write update: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("sync update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close update: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod update: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	return nil
}
