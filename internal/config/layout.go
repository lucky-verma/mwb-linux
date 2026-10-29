// internal/config/layout.go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// SaveLayout rewrites layout, layout_changed and grid in the config file at
// path, keeping every other line, comment and permission as it is. changed <= 0
// removes layout_changed; an empty grid removes the grid line (a plain row).
func SaveLayout(path string, layout []string, grid []string, changed int64) error {
	if len(layout) == 0 {
		return fmt.Errorf("layout is empty")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	text := setLayoutLines(string(data), layout, grid, changed)

	var check Config
	if _, err := toml.Decode(text, &check); err != nil {
		return fmt.Errorf("updated config would not parse: %w", err)
	}
	if !slices.Equal(check.Layout, layout) || !slices.Equal(check.Grid, grid) ||
		(changed > 0 && check.LayoutChanged != changed) {
		return fmt.Errorf("layout did not survive the update")
	}
	return writeConfigAtomic(real, text)
}

func setLayoutLines(text string, layout []string, grid []string, changed int64) string {
	quote := func(ss []string) string {
		q := make([]string, len(ss))
		for i, s := range ss {
			q[i] = strconv.Quote(s)
		}
		return "[" + strings.Join(q, ", ") + "]\n"
	}
	want := map[string]string{"layout": "layout = " + quote(layout)}
	if changed > 0 {
		want["layout_changed"] = "layout_changed = " + strconv.FormatInt(changed, 10) + "\n"
	}
	if len(grid) > 0 {
		want["grid"] = "grid = " + quote(grid)
	}
	return setKeyLines(text, []string{"layout", "layout_changed", "grid"}, want)
}
