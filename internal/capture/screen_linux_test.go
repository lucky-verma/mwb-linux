//go:build linux

package capture

import "testing"

func TestParseXrandrSizeUsesTheWholeDesktop(t *testing.T) {
	extended := `Screen 0: minimum 320 x 200, current 3936 x 1134, maximum 16384 x 16384
eDP-1 connected primary 2016x1134+0+0 (normal left inverted right x axis y axis) 344mm x 194mm
   2016x1134     60.00*+
HDMI-1 connected 1920x1080+2016+0 (normal left inverted right x axis y axis) 527mm x 296mm
   1920x1080     60.00*+  50.00
`
	if got, ok := parseXrandrSize(extended); !ok || got.Width != 3936 || got.Height != 1134 {
		t.Fatalf("extended desktop: got %+v ok=%v, want 3936x1134", got, ok)
	}
	modeOnly := "eDP-1 connected\n   1920x1080     59.96*+\n"
	if got, ok := parseXrandrSize(modeOnly); !ok || got.Width != 1920 {
		t.Fatalf("mode fallback: got %+v ok=%v", got, ok)
	}
	if _, ok := parseXrandrSize("nothing here\n"); ok {
		t.Fatal("parsed a size from nothing")
	}
}

// In group mode on Wayland the edge windows live in c.edges, not c.edge;
// inward motion must still re-arm the edge after connecting.
func TestGroupEdgeWindowsRearmFromMotion(t *testing.T) {
	row := newFakeRow("me", "a", "me")
	c, _ := newGroupCapturer(t, row)
	c.edges = map[string]edgeSource{"left": &fakeEdge{hits: make(chan int32)}}
	c.canSwitch, c.edgeSide = false, ""
	c.trackInward(rearmDistance, 0)
	if !c.canSwitch {
		t.Fatal("group edge windows never re-armed")
	}
}
