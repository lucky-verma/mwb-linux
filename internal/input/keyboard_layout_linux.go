//go:build linux

package input

import (
	"os"
	"os/exec"
	"strings"
)

// ResolveKeyboardLayout returns the inbound keyboard layout profile to use.
// "auto" is best-effort: it detects the local Linux layout and falls back to
// "us" when the session does not expose one.
func ResolveKeyboardLayout(layout string) string {
	canonical := CanonicalKeyboardLayout(layout)
	if canonical != "" && canonical != "auto" {
		return canonical
	}
	if detected := detectKeyboardLayout(); detected != "" {
		return CanonicalKeyboardLayout(detected)
	}
	return "us"
}

// CanonicalKeyboardLayout normalizes common layout names used by config files,
// setxkbmap, and localectl.
func CanonicalKeyboardLayout(layout string) string {
	layout = strings.TrimSpace(strings.ToLower(layout))
	layout = strings.ReplaceAll(layout, "_", "-")
	if i := strings.IndexByte(layout, ','); i >= 0 {
		layout = layout[:i]
	}
	switch {
	case layout == "":
		return ""
	case layout == "auto":
		return "auto"
	case layout == "us" || layout == "en-us" || layout == "usa":
		return "us"
	case layout == "de" || layout == "deu" || layout == "german" || strings.HasPrefix(layout, "de-"):
		return "de"
	case layout == "fr" || layout == "fra" || layout == "french" || strings.HasPrefix(layout, "fr-"):
		return "fr"
	case layout == "be" || layout == "belgian":
		return "be"
	case layout == "es" || layout == "spa" || layout == "spanish" || strings.HasPrefix(layout, "es-"):
		return "es"
	case layout == "la" || layout == "latam" || layout == "latin-american" || layout == "es-419":
		return "es"
	case layout == "it" || layout == "ita" || layout == "italian" || strings.HasPrefix(layout, "it-"):
		return "it"
	case layout == "gb" || layout == "uk" || layout == "en-gb" || layout == "british":
		return "gb"
	case layout == "pt" || layout == "por" || layout == "portuguese" || strings.HasPrefix(layout, "pt-"):
		return "pt"
	case layout == "no" || layout == "nb" || layout == "nn" || layout == "norwegian":
		return "nordic"
	case layout == "dk" || layout == "da" || layout == "danish":
		return "nordic"
	case layout == "se" || layout == "sv" || layout == "swedish":
		return "nordic"
	case layout == "fi" || layout == "finnish":
		return "nordic"
	case layout == "ch" || layout == "de-ch" || layout == "fr-ch" || layout == "swiss" ||
		layout == "swiss-german" || layout == "swiss-french":
		return "ch"
	case layout == "nl" || layout == "dutch":
		return "nl"
	default:
		return layout
	}
}

func detectKeyboardLayout() string {
	if layout := os.Getenv("XKB_DEFAULT_LAYOUT"); layout != "" {
		return layout
	}
	if layout := detectSetxkbmapLayout(); layout != "" {
		return layout
	}
	return detectLocalectlLayout()
}

func detectSetxkbmapLayout() string {
	out, err := exec.Command("setxkbmap", "-query").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "layout" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func detectLocalectlLayout() string {
	out, err := exec.Command("localectl", "status").Output()
	if err != nil {
		return ""
	}
	for _, want := range []string{"X11 Layout", "VC Keymap"} {
		for _, line := range strings.Split(string(out), "\n") {
			key, val, ok := strings.Cut(line, ":")
			if ok && strings.TrimSpace(key) == want {
				return strings.TrimSpace(val)
			}
		}
	}
	return ""
}
