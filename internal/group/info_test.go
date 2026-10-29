package group

import (
	"slices"
	"testing"
	"time"
)

func TestInfoRoundTripKeepsTheChoiceTime(t *testing.T) {
	sent := time.Unix(1_700_000_000, 0)
	l := Layout{Order: []string{"a", "b", "c"}, Changed: sent.Unix() - 90, By: "b"}
	forgotten := map[string]int64{"d": sent.Unix() - 30}
	doc, err := encodeInfo(l, map[string]string{"c": "10.0.0.3"}, forgotten, 1920, 1080, true, 42, 15100, sent)
	if err != nil {
		t.Fatal(err)
	}
	// Arrives on a machine whose clock is an hour ahead: the age still holds.
	recv := sent.Add(time.Hour)
	in, got, err := decodeInfo(doc, recv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Order, l.Order) || got.By != "b" || got.Changed != recv.Unix()-90 {
		t.Fatalf("layout %+v", got)
	}
	if in.Members["c"] != "10.0.0.3" || in.Width != 1920 || !in.Sharing || in.Epoch != 42 || in.Port != 15100 {
		t.Fatalf("info %+v", in)
	}
	// The tombstone survives the same clock skew as an age.
	if in.Forgotten["d"] != recv.Unix()-30 {
		t.Fatalf("forgotten time not preserved across clock skew: %+v", in.Forgotten)
	}
}

func TestInfoCarriesTwoDimensionalPositions(t *testing.T) {
	l := Layout{
		Order: []string{"a", "b", "c"},
		Pos:   map[string]Pos{"a": {0, 0}, "b": {1, 0}, "c": {1, 1}},
	}
	doc, err := encodeInfo(l, nil, nil, 0, 0, false, 0, 0, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := decodeInfo(doc, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !samePos(got.Pos, l.Pos) {
		t.Fatalf("positions did not round-trip: %+v", got.Pos)
	}

	// A document from a build that knows only the row (no pos) decodes to a row
	// layout: Pos nil, Grid derived from Order.
	rowDoc, _ := encodeInfo(Layout{Order: []string{"a", "b"}}, nil, nil, 0, 0, false, 0, 0, time.Now())
	_, row, _ := decodeInfo(rowDoc, time.Now())
	if row.Pos != nil {
		t.Fatalf("a row document produced positions: %+v", row.Pos)
	}
	if g := row.Grid(); g["a"] != (Pos{0, 0}) || g["b"] != (Pos{1, 0}) {
		t.Fatalf("row Grid() wrong: %+v", g)
	}

	// Out-of-bounds or unnamed positions are dropped, not trusted.
	bad := []byte(`{"v":1,"layout":["a"],"layout_age":-1,"pos":{"a":{"c":0,"r":0},"":{"c":1,"r":0},"b":{"c":99,"r":0}}}`)
	_, l2, err := decodeInfo(bad, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(l2.Pos) != 1 || l2.Pos["a"] != (Pos{0, 0}) {
		t.Fatalf("implausible positions kept: %+v", l2.Pos)
	}
}

func TestInfoUnknownChoiceAndBadInput(t *testing.T) {
	doc, _ := encodeInfo(Layout{Order: []string{"a"}}, nil, nil, 0, 0, false, 0, 0, time.Now())
	if _, l, err := decodeInfo(doc, time.Now()); err != nil || l.Changed != 0 {
		t.Fatalf("unknown age: %+v %v", l, err)
	}
	if _, _, err := decodeInfo([]byte(`{"v":99}`), time.Now()); err == nil {
		t.Fatal("an unknown version was accepted")
	}
	in, l, err := decodeInfo([]byte(`{"v":1,"layout":["ok","","this-name-is-far-too-long"],"layout_age":-1,"members":{"":"x"},"w":-4}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.Order, []string{"ok"}) || len(in.Members) != 0 || in.Width != 0 {
		t.Fatalf("implausible values kept: %+v %+v", l, in)
	}
}
