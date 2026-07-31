package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  [3]int
		valid bool
	}{
		{"v0.5.1", [3]int{0, 5, 1}, true},
		{"0.5.1", [3]int{0, 5, 1}, true},
		{"v1.0", [3]int{1, 0, 0}, true},
		{"v2", [3]int{2, 0, 0}, true},
		{"v1.2.3-rc1", [3]int{1, 2, 3}, true},
		{"v1.2.3+meta", [3]int{1, 2, 3}, true},
		{"dev", [3]int{}, false},
		{"", [3]int{}, false},
		{"v1.2.3.4", [3]int{}, false},
		{"vX.Y.Z", [3]int{}, false},
	} {
		got, ok := ParseVersion(tc.in)
		if ok != tc.valid {
			t.Errorf("ParseVersion(%q) ok = %v, want %v", tc.in, ok, tc.valid)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParseVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.5.1", "v0.5.2", true},
		{"v0.5.1", "v0.6.0", true},
		{"v0.5.1", "v1.0.0", true},
		{"v0.5.1", "v0.5.1", false},
		{"v0.5.2", "v0.5.1", false},
		{"v1.0.0", "v0.9.9", false},
		// A locally built binary should still be offered a real release.
		{"dev", "v0.5.1", true},
		// A malformed upstream tag must never trigger an install.
		{"v0.5.1", "garbage", false},
	} {
		if got := IsNewer(tc.current, tc.latest); got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	in := `d0b4f1a2  mwb-linux-amd64
9f8e7d6c  mwb-linux-arm64
abc12345 *mwb-linux_0.5.1_amd64.deb
`
	sums, err := ParseChecksums(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if sums["mwb-linux-amd64"] != "d0b4f1a2" {
		t.Errorf("amd64 = %q", sums["mwb-linux-amd64"])
	}
	// The BSD-style "*" binary marker must be stripped from the filename.
	if sums["mwb-linux_0.5.1_amd64.deb"] != "abc12345" {
		t.Errorf("deb entry not parsed: %v", sums)
	}
}

func TestParseChecksums_EmptyIsAnError(t *testing.T) {
	if _, err := ParseChecksums(strings.NewReader("\n\n")); err == nil {
		t.Error("empty checksums must be an error, not an empty map that verifies nothing")
	}
}

// The asset name is a contract with .goreleaser.yaml. If the release template
// changes, `mwb update` silently stops finding its own artifact.
func TestAssetName_MatchesGoreleaserTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Skipf("cannot read .goreleaser.yaml: %v", err)
	}
	const want = `name_template: "mwb-linux-{{ .Arch }}"`
	if !strings.Contains(string(data), want) {
		t.Errorf("goreleaser archive template changed; AssetName() builds %q from "+
			"the old template and will no longer match the published asset.\n"+
			"Expected to find: %s", AssetName(), want)
	}
	if !strings.HasPrefix(AssetName(), "mwb-linux-") {
		t.Errorf("AssetName() = %q, want the mwb-linux-<arch> form", AssetName())
	}
}

// A self-updater downloads code and runs it. Anything not clearly GitHub must
// be refused before a byte is fetched.
func TestGet_RejectsUntrustedSources(t *testing.T) {
	for _, rawURL := range []string{
		"http://github.com/foo",            // plaintext
		"https://evil.example.com/mwb",     // wrong host
		"https://github.com.evil.test/mwb", // suffix-confusion host
		"ftp://github.com/mwb",             // wrong scheme
	} {
		if _, err := get(context.Background(), rawURL); err == nil {
			t.Errorf("get(%q) succeeded; untrusted sources must be refused", rawURL)
		}
	}
}

func TestReplaceBinary_IsAtomicAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mwb")
	if err := os.WriteFile(dest, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(dest, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("content = %q, want %q", got, "new binary")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755 — the replacement must stay executable", info.Mode().Perm())
	}

	// No temp files may be left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mwb-update-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}
