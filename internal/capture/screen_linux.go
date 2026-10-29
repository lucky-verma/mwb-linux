//go:build linux

package capture

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetScreenSizeXrandr detects screen size via xrandr.
func GetScreenSizeXrandr() ScreenInfo {
	if s, ok := DetectScreenSize(); ok {
		return s
	}
	return ScreenInfo{Width: 1920, Height: 1080}
}

// DetectScreenSize is GetScreenSizeXrandr without the 1920x1080 fallback, for
// callers that must not report a guess as a measurement (a Linux peer uses
// what it is told as the real size of this screen).
func DetectScreenSize() (ScreenInfo, bool) {
	out, err := exec.Command("xrandr", "--current").Output()
	if err != nil {
		return ScreenInfo{}, false
	}
	return parseXrandrSize(string(out))
}

// parseXrandrSize reads the whole desktop's size: the "current W x H" of the
// screen, which spans every monitor of an extended desktop. The cursor
// crosses at that desktop's outer edge, and absolute input covers all of it,
// so one monitor's mode is the wrong size whenever a second one is attached.
// The first active mode is the fallback for output without a Screen line.
func parseXrandrSize(out string) (ScreenInfo, bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "Screen ") {
			continue
		}
		if i := strings.Index(line, "current "); i >= 0 {
			var w, h int32
			if n, _ := fmt.Sscanf(line[i:], "current %d x %d", &w, &h); n == 2 && w > 0 && h > 0 {
				return ScreenInfo{Width: w, Height: h}, true
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "*") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				parts := strings.SplitN(fields[0], "x", 2)
				if len(parts) == 2 {
					var w, h int32
					_, _ = fmt.Sscanf(parts[0], "%d", &w)
					_, _ = fmt.Sscanf(parts[1], "%d", &h)
					if w > 0 && h > 0 {
						return ScreenInfo{Width: w, Height: h}, true
					}
				}
			}
		}
	}
	return ScreenInfo{}, false
}
