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
