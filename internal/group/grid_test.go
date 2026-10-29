package group

import (
	"reflect"
	"testing"
)

func TestOppositeAllSides(t *testing.T) {
	for side, want := range map[string]string{
		"left": "right", "right": "left", "up": "down", "down": "up", "": "", "x": "",
	} {
		if got := Opposite(side); got != want {
			t.Errorf("Opposite(%q) = %q, want %q", side, got, want)
		}
	}
}

func TestRowGridIsOneRow(t *testing.T) {
	// The layout the group uses today, read as a 1×N grid: every screen on
	// row 0, left to right, duplicates and blanks dropped.
	g := RowGrid([]string{"a", "b", "", "c", "a"})
	want := map[string]Pos{"a": {0, 0}, "b": {1, 0}, "c": {2, 0}}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("RowGrid = %v, want %v", g, want)
	}
}

func TestNeighborInEachDirection(t *testing.T) {
	// me in the middle of a plus shape.
	grid := map[string]Pos{
		"me":   {1, 1},
		"west": {0, 1}, "east": {2, 1},
		"north": {1, 0}, "south": {1, 2},
	}
	for side, want := range map[string]string{
		"left": "west", "right": "east", "up": "north", "down": "south",
	} {
		got, ok := Neighbor(grid, "me", side)
		if !ok || got != want {
			t.Errorf("Neighbor(me, %q) = %q,%v; want %q,true", side, got, ok, want)
		}
	}
	// No screen below the corner "south".
	if n, ok := Neighbor(grid, "south", "down"); ok {
		t.Errorf("Neighbor(south, down) = %q; want none", n)
	}
	// An unknown side or machine is no neighbour.
	if _, ok := Neighbor(grid, "me", "sideways"); ok {
		t.Error("an unknown side found a neighbour")
	}
	if _, ok := Neighbor(grid, "ghost", "left"); ok {
		t.Error("an unknown machine found a neighbour")
	}
}

func TestEdgesReportsSharedSides(t *testing.T) {
	// A 2×2 block: each corner borders two others.
	grid := map[string]Pos{"tl": {0, 0}, "tr": {1, 0}, "bl": {0, 1}, "br": {1, 1}}
	got := Edges(grid, "tl")
	want := map[string]bool{"right": true, "down": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Edges(tl) = %v, want %v", got, want)
	}
	// A lone screen borders nothing.
	if e := Edges(map[string]Pos{"solo": {0, 0}}, "solo"); len(e) != 0 {
		t.Fatalf("a lone screen has edges: %v", e)
	}
	// A middle screen in a row borders left and right only, as today.
	row := RowGrid([]string{"a", "b", "c"})
	if e := Edges(row, "b"); !reflect.DeepEqual(e, map[string]bool{"left": true, "right": true}) {
		t.Fatalf("row middle edges = %v", e)
	}
}

func TestBeyondGridSkipsOfflineAndStopsAtGaps(t *testing.T) {
	// A row with a gap at col 2: a, b, (empty), d.
	grid := map[string]Pos{"a": {0, 0}, "b": {1, 0}, "d": {3, 0}}
	all := func(string) bool { return true }

	// From a, right reaches b; b is offline, so skip to... nothing, because the
	// next cell (col 2) is empty: the pointer cannot cross the gap to d.
	offlineB := func(n string) bool { return n != "b" }
	if n, ok := BeyondGrid(grid, offlineB, "a", "right"); ok {
		t.Fatalf("crossed a gap to reach %q", n)
	}
	// With b online, from a the first machine to the right is b.
	if n, ok := BeyondGrid(grid, all, "a", "right"); !ok || n != "b" {
		t.Fatalf("BeyondGrid(a, right) = %q,%v; want b,true", n, ok)
	}
	// A contiguous row skips an offline middle machine and keeps going.
	row := RowGrid([]string{"a", "b", "c"})
	if n, ok := BeyondGrid(row, func(x string) bool { return x != "b" }, "a", "right"); !ok || n != "c" {
		t.Fatalf("BeyondGrid over an offline b = %q,%v; want c,true", n, ok)
	}
	// Vertical works the same.
	col := map[string]Pos{"top": {0, 0}, "mid": {0, 1}, "bot": {0, 2}}
	if n, ok := BeyondGrid(col, func(x string) bool { return x != "mid" }, "top", "down"); !ok || n != "bot" {
		t.Fatalf("BeyondGrid(top, down) over offline mid = %q,%v; want bot,true", n, ok)
	}
	// Off the edge: nothing beyond the last cell.
	if _, ok := BeyondGrid(col, all, "bot", "down"); ok {
		t.Error("found a machine past the bottom edge")
	}
}

func TestParseAndFormatGrid(t *testing.T) {
	pos := map[string]Pos{"a": {0, 0}, "b": {1, 0}, "c": {1, 1}}
	got := ParseGrid(FormatGrid(pos))
	if !reflect.DeepEqual(got, pos) {
		t.Fatalf("round-trip changed positions: %v", got)
	}
	// FormatGrid is stable (sorted).
	if f := FormatGrid(pos); !reflect.DeepEqual(f, []string{"a:0:0", "b:1:0", "c:1:1"}) {
		t.Fatalf("FormatGrid = %v", f)
	}
	// Bad entries are dropped; empty input gives nil.
	if ParseGrid(nil) != nil || FormatGrid(nil) != nil {
		t.Fatal("empty input should give nil")
	}
	bad := ParseGrid([]string{"a:0:0", "nope", "b:x:0", "c:9:9", ":1:1", "d:1:1"})
	if !reflect.DeepEqual(bad, map[string]Pos{"a": {0, 0}, "d": {1, 1}}) {
		t.Fatalf("bad entries not dropped: %v", bad)
	}
}
