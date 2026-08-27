//go:build linux

package clipboard

import (
	"net/url"
	"strings"
)

// fileURIPrefix is the only scheme worth acting on. A copy from a remote
// filesystem view can offer other schemes, which MWB does not transfer either.
const (
	fileURIPrefix   = "file://"
	uriListTarget   = "text/uri-list"
	gnomeFileTarget = "x-special/gnome-copied-files"
)

// getLocalFileClipboard returns local paths when the desktop clipboard holds a file
// selection, and nil otherwise.
//
// File managers advertise a copied file as text/uri-list. They also usually
// offer text/plain holding the same path, which is why this is checked before
// the text path: sending the filename as text instead of the file is the wrong
// behaviour and is what happens without this.
func (m *Manager) getLocalFileClipboard() []string {
	if m.backend == nil {
		return nil
	}
	return m.backend.readFiles()
}

func clipboardFileTarget(targets string) string {
	// GNOME's target includes whether the operation is copy or cut and is what
	// Nautilus consumes for Ctrl+V. Prefer it when both are offered.
	if hasClipboardTarget(targets, gnomeFileTarget) {
		return gnomeFileTarget
	}
	if hasClipboardTarget(targets, uriListTarget) {
		return uriListTarget
	}
	return ""
}

// parseFileURIs extracts local filesystem paths from a text/uri-list payload.
// Separated from the xclip call so the parsing is testable on its own.
func parseFileURIs(list string) []string {
	var paths []string
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		// A uri-list may carry "copy"/"cut" as its first line (GNOME) and
		// comment lines beginning '#' per RFC 2483.
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, fileURIPrefix) {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Path == "" {
			continue
		}
		// url.Parse already percent-decodes Path, which is what turns
		// "my%20file.txt" back into "my file.txt".
		paths = append(paths, u.Path)
	}
	return paths
}
