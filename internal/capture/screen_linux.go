//go:build linux

package capture

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetScreenSizeXrandr detects screen size via xrandr.
func GetScreenSizeXrandr() ScreenInfo {
	out, err := exec.Command("xrandr", "--current").Output()
	if err != nil {
		return ScreenInfo{Width: 1920, Height: 1080}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "*") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				parts := strings.SplitN(fields[0], "x", 2)
				if len(parts) == 2 {
					var w, h int32
					fmt.Sscanf(parts[0], "%d", &w)
					fmt.Sscanf(parts[1], "%d", &h)
					if w > 0 && h > 0 {
						return ScreenInfo{Width: w, Height: h}
					}
				}
			}
		}
	}
	return ScreenInfo{Width: 1920, Height: 1080}
}
