//go:build linux

package clipboard

import "testing"

func TestFileTargetsCoverEveryFileManager(t *testing.T) {
	got := fileTargets("/tmp/some dir/a.pdf")
	want := map[string]string{
		gnomeFileTarget: "copy\nfile:///tmp/some%20dir/a.pdf",
		mateFileTarget:  "copy\nfile:///tmp/some%20dir/a.pdf",
		uriListTarget:   "file:///tmp/some%20dir/a.pdf\r\n",
		"UTF8_STRING":   "/tmp/some dir/a.pdf",
	}
	for target, payload := range want {
		if string(got[target]) != payload {
			t.Errorf("%s = %q, want %q", target, got[target], payload)
		}
	}
}

func TestCajaCopiesAreRecognised(t *testing.T) {
	if got := clipboardFileTarget("TARGETS\nx-special/mate-copied-files\nUTF8_STRING\n"); got != mateFileTarget {
		t.Fatalf("got %q", got)
	}
	if got := preferredFileClipboardTarget("MATE"); got != mateFileTarget {
		t.Fatalf("xclip fallback on MATE offers %q", got)
	}
}
