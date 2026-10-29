package group

import (
	"encoding/json"
	"errors"
	"time"
)

// Info is the group document one machine sends another: its view of the
// layout, the members it knows with the address it reaches each at, and its
// own screen and sharing mode.
type Info struct {
	V int `json:"v"`
	// Layout and how long ago it was chosen, in seconds (-1 = unknown).
	Layout    []string `json:"layout"`
	LayoutAge int64    `json:"layout_age"`
	LayoutBy  string   `json:"layout_by,omitempty"`
	// Pos is the two-dimensional arrangement, when the sender has one. Absent
	// for a plain row, and ignored by a build that only knows the row, so a
	// mixed group keeps working: an older machine reads Layout, a newer one
	// prefers Pos. Travels with the layout it belongs to.
	Pos map[string]Pos `json:"pos,omitempty"`
	// Members maps machine names to an address the sender reaches them at.
	Members map[string]string `json:"members,omitempty"`
	// Forgotten maps a machine the sender was told to forget to how long ago
	// it was forgotten, in seconds, so a peer that still remembers that machine
	// drops it too instead of gossiping it back. Ages, not times, so clocks
	// need not agree, exactly as LayoutAge.
	Forgotten map[string]int64 `json:"forgotten,omitempty"`
	Width     int32            `json:"w,omitempty"`
	Height    int32            `json:"h,omitempty"`
	Sharing   bool             `json:"sharing"`
	// Epoch is the highest cursor announcement the sender has seen, so a
	// machine that just started numbers its next one above it.
	Epoch uint32 `json:"epoch,omitempty"`
	// Port is the sender's own control port, for members that do not all
	// use the default one.
	Port int `json:"port,omitempty"`
}

const infoVersion = 1

// maxNameLen matches the wire's machine-name field.
const maxNameLen = 15

// encodeInfo turns a local Layout into a document, ages relative to now.
func encodeInfo(l Layout, members map[string]string, forgotten map[string]int64, w, h int32, sharing bool, epoch uint32, port int, now time.Time) ([]byte, error) {
	age := int64(-1)
	if l.Changed > 0 {
		age = max(0, now.Unix()-l.Changed)
	}
	var ages map[string]int64
	if len(forgotten) > 0 {
		ages = make(map[string]int64, len(forgotten))
		for n, at := range forgotten {
			ages[n] = max(0, now.Unix()-at)
		}
	}
	var pos map[string]Pos
	if len(l.Pos) > 0 {
		pos = make(map[string]Pos, len(l.Pos))
		for n, p := range l.Pos {
			pos[n] = p
		}
	}
	return json.Marshal(Info{
		V: infoVersion, Layout: l.Order, LayoutAge: age, LayoutBy: l.By, Pos: pos,
		Members: members, Forgotten: ages, Width: w, Height: h, Sharing: sharing, Epoch: epoch, Port: port,
	})
}

// decodeInfo parses a document and returns its layout in local time. Names
// that could not be real machine names are dropped rather than trusted.
func decodeInfo(doc []byte, now time.Time) (Info, Layout, error) {
	var in Info
	if err := json.Unmarshal(doc, &in); err != nil {
		return Info{}, Layout{}, err
	}
	if in.V != infoVersion {
		return Info{}, Layout{}, errors.New("unknown group document version")
	}
	l := Layout{By: in.LayoutBy}
	for _, n := range in.Layout {
		if validName(n) && len(l.Order) < MaxMachines {
			l.Order = append(l.Order, n)
		}
	}
	if in.LayoutAge >= 0 {
		l.Changed = now.Unix() - in.LayoutAge
	}
	// Two-dimensional positions, when the sender sent any. Keep only real names
	// within the arrangement bounds, so a bad document cannot place a screen far
	// off the grid.
	if len(in.Pos) > 0 {
		pos := make(map[string]Pos, len(in.Pos))
		for n, p := range in.Pos {
			if validName(n) && p.Col >= 0 && p.Row >= 0 && p.Col < MaxMachines && p.Row < MaxMachines && len(pos) < MaxMachines {
				pos[n] = p
			}
		}
		in.Pos = pos
		if len(pos) > 0 {
			l.Pos = pos
		}
	} else {
		in.Pos = nil
	}
	members := map[string]string{}
	for n, addr := range in.Members {
		if validName(n) && len(addr) <= 255 && len(members) < MaxMachines {
			members[n] = addr
		}
	}
	in.Members = members
	forgotten := map[string]int64{}
	for n, age := range in.Forgotten {
		if validName(n) && age >= 0 && len(forgotten) < maxForgotten {
			forgotten[n] = now.Unix() - age
		}
	}
	in.Forgotten = forgotten
	if in.Port < 0 || in.Port > 65535 {
		in.Port = 0
	}
	if in.Width < 0 || in.Height < 0 || in.Width > 65535 || in.Height > 65535 {
		in.Width, in.Height = 0, 0
	}
	return in, l, nil
}

func validName(n string) bool {
	return n != "" && len(n) <= maxNameLen
}
