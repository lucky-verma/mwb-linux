// Package group runs a group of up to six mwb-linux machines whose screens
// sit in one row. Every machine connects to every other one, they share the
// row's order (the layout) and the single cursor's location, and any machine
// whose keyboard and mouse are used drives that cursor.
package group

import (
	"slices"
	"sort"
)

// MaxMachines is the largest group, this machine included.
const MaxMachines = 6

// Layout is the arrangement of the screens, by machine name.
type Layout struct {
	// Order is the left-to-right row. It is always kept as the row projection
	// of the arrangement, so a machine that knows nothing of two dimensions
	// (and an older build) still sees a sensible sequence.
	Order []string
	// Pos is the two-dimensional arrangement: each machine's column (left to
	// right) and row (top to bottom). Empty means the arrangement is simply the
	// one row in Order; then Grid derives positions from it. When Pos is set it
	// is authoritative, and Order is its row projection.
	Pos map[string]Pos
	// Changed is when the order was chosen, in this machine's Unix seconds;
	// 0 means unknown, older than any known choice. Peers send an age rather
	// than a time, so clocks need not agree.
	Changed int64
	// By names the machine that made the choice; it settles a tie.
	By string
}

// Grid returns the effective two-dimensional arrangement: Pos when set, else
// the single row in Order. A machine in Order but missing from Pos (one that
// joined after the arrangement was set) is given a cell on a new column in the
// top row, so every member always has a place and stays reachable. The result
// is a fresh map the caller may keep.
func (l Layout) Grid() map[string]Pos {
	if len(l.Pos) == 0 {
		return RowGrid(l.Order)
	}
	out := make(map[string]Pos, len(l.Pos))
	maxCol := -1
	for n, p := range l.Pos {
		out[n] = p
		if p.Col > maxCol {
			maxCol = p.Col
		}
	}
	for _, n := range l.Order {
		if n == "" {
			continue
		}
		if _, ok := out[n]; !ok {
			maxCol++
			out[n] = Pos{Col: maxCol, Row: 0}
		}
	}
	return out
}

// Newer reports whether l is a more recent choice than other. Every machine
// decides the same way on the same pair, so the group converges on one.
func (l Layout) Newer(other Layout) bool {
	if l.Changed != other.Changed {
		return l.Changed > other.Changed
	}
	if l.By != other.By {
		return l.By < other.By
	}
	return false
}

// Equal reports whether both describe the same choice.
func (l Layout) Equal(other Layout) bool {
	return l.Changed == other.Changed && l.By == other.By &&
		slices.Equal(l.Order, other.Order) && samePos(l.Pos, other.Pos)
}

// samePos reports whether two position maps hold the same cells.
func samePos(a, b map[string]Pos) bool {
	if len(a) != len(b) {
		return false
	}
	for n, p := range a {
		if q, ok := b[n]; !ok || q != p {
			return false
		}
	}
	return true
}

// Complete returns order with every machine in names present exactly once.
// Machines it lacks are added on the right in name order, so machines that
// complete the same order independently get the same result. A machine with
// no layout yet and a single peer places that peer on legacyEdge, the side
// the two-machine setting chose ("left" or "right"). added reports a change.
func Complete(order []string, names []string, me, legacyEdge string) (out []string, added bool) {
	seen := map[string]bool{}
	for _, n := range order {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	var missing []string
	for _, n := range append([]string{me}, names...) {
		if n != "" && !seen[n] {
			seen[n] = true
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return out, len(out) != len(order)
	}
	sort.Strings(missing)
	if len(out) <= 1 && len(names) == 1 && names[0] != me && (len(out) == 0 || out[0] == me) {
		// Two machines that were set up before layouts existed: keep the side.
		switch legacyEdge {
		case "left":
			return []string{names[0], me}, true
		case "right":
			return []string{me, names[0]}, true
		}
	}
	return append(out, missing...), true
}

// Beyond returns the first machine past side ("left" or "right") of from in
// order for which online reports true, skipping the ones that are not.
func Beyond(order []string, online func(string) bool, from, side string) (string, bool) {
	i := slices.Index(order, from)
	if i < 0 {
		return "", false
	}
	step := 1
	if side == "left" {
		step = -1
	} else if side != "right" {
		return "", false
	}
	for j := i + step; j >= 0 && j < len(order); j += step {
		if online(order[j]) {
			return order[j], true
		}
	}
	return "", false
}

// Neighbors reports whether any machine sits left or right of me in order,
// online or not: where this machine's screen has a shared edge at all.
func Neighbors(order []string, me string) (left, right bool) {
	i := slices.Index(order, me)
	if i < 0 {
		return false, false
	}
	return i > 0, i < len(order)-1
}

// Opposite returns the other side, for all four directions.
func Opposite(side string) string {
	switch side {
	case "left":
		return "right"
	case "right":
		return "left"
	case "up":
		return "down"
	case "down":
		return "up"
	}
	return ""
}
