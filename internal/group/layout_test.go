package group

import (
	"slices"
	"testing"
)

func TestLayoutGridAndEqualWithPositions(t *testing.T) {
	// Grid: a row layout derives positions; a 2-D layout returns its own.
	row := Layout{Order: []string{"a", "b", "c"}}
	if g := row.Grid(); g["a"] != (Pos{0, 0}) || g["c"] != (Pos{2, 0}) {
		t.Fatalf("row Grid() = %+v", g)
	}
	twoD := Layout{Order: []string{"a", "b"}, Pos: map[string]Pos{"a": {0, 0}, "b": {0, 1}}}
	if g := twoD.Grid(); g["b"] != (Pos{0, 1}) {
		t.Fatalf("2-D Grid() = %+v", g)
	}

	// A machine that joined after the arrangement was set (in Order, not in Pos)
	// still gets a cell, so it stays reachable.
	joined := Layout{
		Order: []string{"a", "b", "c"},
		Pos:   map[string]Pos{"a": {0, 0}, "b": {0, 1}},
	}
	g := joined.Grid()
	if len(g) != 3 {
		t.Fatalf("a newly joined machine was dropped from the grid: %+v", g)
	}
	if _, ok := g["c"]; !ok || g["c"].Row != 0 {
		t.Fatalf("newcomer c has no top-row cell: %+v", g)
	}
	if g["c"] == g["a"] || g["c"] == g["b"] {
		t.Fatalf("newcomer c shares a cell: %+v", g)
	}

	// Equal: positions are part of the identity of a choice.
	base := Layout{Order: []string{"a", "b"}, Pos: map[string]Pos{"a": {0, 0}, "b": {0, 1}}, Changed: 5, By: "a"}
	same := Layout{Order: []string{"a", "b"}, Pos: map[string]Pos{"a": {0, 0}, "b": {0, 1}}, Changed: 5, By: "a"}
	diff := Layout{Order: []string{"a", "b"}, Pos: map[string]Pos{"a": {0, 0}, "b": {1, 0}}, Changed: 5, By: "a"}
	if !base.Equal(same) {
		t.Fatal("identical layouts with the same positions should be equal")
	}
	if base.Equal(diff) {
		t.Fatal("different positions must make layouts unequal")
	}
	// A row and the same machines placed in that row are different objects but
	// the row has no explicit Pos, so Equal distinguishes them (one has Pos).
	if base.Equal(Layout{Order: []string{"a", "b"}, Changed: 5, By: "a"}) {
		t.Fatal("a layout with positions differs from the bare row")
	}
}

func TestLayoutNewer(t *testing.T) {
	old := Layout{Order: []string{"a", "b"}, Changed: 100, By: "a"}
	newer := Layout{Order: []string{"b", "a"}, Changed: 200, By: "b"}
	if !newer.Newer(old) || old.Newer(newer) {
		t.Fatal("the later choice must win")
	}
	if !old.Newer(Layout{Order: []string{"x"}}) {
		t.Fatal("any known choice beats an unknown one")
	}
	tieA := Layout{Changed: 5, By: "a"}
	tieB := Layout{Changed: 5, By: "b"}
	if tieA.Newer(tieB) == tieB.Newer(tieA) {
		t.Fatal("a tie must be settled one way")
	}
	if tieA.Newer(tieA) {
		t.Fatal("a choice is not newer than itself")
	}
}

func TestCompleteAddsMissingMachinesTheSameWayEverywhere(t *testing.T) {
	// Two machines that each know a different subset end up with one order.
	got1, _ := Complete([]string{"a", "b"}, []string{"b", "d", "c"}, "a", "")
	got2, _ := Complete([]string{"a", "b"}, []string{"a", "c", "d"}, "b", "")
	want := []string{"a", "b", "c", "d"}
	if !slices.Equal(got1, want) || !slices.Equal(got2, want) {
		t.Fatalf("got %v and %v, want %v", got1, got2, want)
	}
	if _, added := Complete(want, []string{"c"}, "a", ""); added {
		t.Fatal("a complete order reported a change")
	}
	if got, added := Complete([]string{"a", "", "a", "b"}, nil, "a", ""); !added || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("duplicates not removed: %v %v", got, added)
	}
}

func TestCompleteKeepsTheTwoMachineSide(t *testing.T) {
	// A pair set up before layouts: "edge = left" on laptop means the peer is
	// on its left, and the peer had "edge = right". Both reach one order.
	onLaptop, _ := Complete(nil, []string{"desk"}, "laptop", "left")
	onPeer, _ := Complete(nil, []string{"laptop"}, "desk", "right")
	want := []string{"desk", "laptop"}
	if !slices.Equal(onLaptop, want) || !slices.Equal(onPeer, want) {
		t.Fatalf("got %v and %v, want %v", onLaptop, onPeer, want)
	}
}

func TestBeyondSkipsOfflineMachines(t *testing.T) {
	order := []string{"a", "b", "c", "d"}
	up := map[string]bool{"a": true, "b": false, "c": true, "d": true}
	online := func(n string) bool { return up[n] }
	for _, tc := range []struct {
		from, side, want string
		ok               bool
	}{
		{"a", "right", "c", true},
		{"c", "left", "a", true},
		{"d", "right", "", false},
		{"a", "left", "", false},
		{"x", "right", "", false},
		{"a", "up", "", false},
	} {
		got, ok := Beyond(order, online, tc.from, tc.side)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Beyond(%s, %s) = %q %v, want %q %v", tc.from, tc.side, got, ok, tc.want, tc.ok)
		}
	}
	if l, r := Neighbors(order, "a"); l || !r {
		t.Fatal("the leftmost machine has only a right neighbour")
	}
	if l, r := Neighbors(order, "b"); !l || !r {
		t.Fatal("a middle machine has both")
	}
}
