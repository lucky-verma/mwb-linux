//go:build linux

package clipboard

import (
	"reflect"
	"testing"
)

func TestParseFileURIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"single file", "file:///home/u/a.txt\n", []string{"/home/u/a.txt"}},
		{
			// GNOME prefixes the list with the operation.
			"gnome copy prefix",
			"copy\nfile:///home/u/a.txt\n",
			[]string{"/home/u/a.txt"},
		},
		{
			// url.Parse percent-decodes, which is what restores real names.
			"percent encoded name",
			"file:///home/u/my%20file%20(1).txt\n",
			[]string{"/home/u/my file (1).txt"},
		},
		{"rfc2483 comment", "# comment\nfile:///a\n", []string{"/a"}},
		{"multiple", "file:///a\nfile:///b\n", []string{"/a", "/b"}},
		{"non-file scheme ignored", "https://example.com/x\n", nil},
		{"empty", "", nil},
		{"whitespace only", "\n \n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFileURIs(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFileURIs(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestClipboardFileTargetPrefersGNOME(t *testing.T) {
	targets := "TARGETS\ntext/uri-list\nx-special/gnome-copied-files\n"
	if got := clipboardFileTarget(targets); got != gnomeFileTarget {
		t.Fatalf("target = %q, want %q", got, gnomeFileTarget)
	}
	if got := clipboardFileTarget("TARGETS\ntext/uri-list\n"); got != uriListTarget {
		t.Fatalf("fallback target = %q, want %q", got, uriListTarget)
	}
}

// TestFileClipHashDetectsRecopy locks in the fix for a repeat copy of the same
// file: the path alone cannot distinguish a fresh copy from the clipboard
// simply still holding the file, so the copy generation (raised by the XFIXES
// watcher on each real copy) is part of the identity.
func TestFileClipHashDetectsRecopy(t *testing.T) {
	paths := []string{"/home/u/doc.docx"}

	// The clipboard sitting unchanged: same generation, same hash -> no re-send.
	if fileClipHash(7, paths) != fileClipHash(7, paths) {
		t.Fatal("an unchanged clipboard must hash the same, or it would re-send every poll")
	}
	// A fresh copy of the same file: the watcher bumps the generation, so the
	// hash differs and the file is sent again.
	if fileClipHash(7, paths) == fileClipHash(8, paths) {
		t.Fatal("a repeat copy (new generation) must differ, or it would never re-send")
	}
	// A different file is distinct regardless of generation.
	if fileClipHash(7, paths) == fileClipHash(7, []string{"/home/u/other.docx"}) {
		t.Fatal("different files must hash differently")
	}
}
