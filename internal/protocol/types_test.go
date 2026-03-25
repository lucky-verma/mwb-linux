// internal/protocol/types_test.go
package protocol

import "testing"

func TestPackageTypeValues(t *testing.T) {
	tests := []struct {
		name string
		pt   PackageType
		want byte
	}{
		{"Mouse", Mouse, 123},
		{"Keyboard", Keyboard, 122},
		{"Handshake", Handshake, 126},
		{"HandshakeAck", HandshakeAck, 127},
		{"Heartbeat", Heartbeat, 20},
		{"ByeBye", ByeBye, 4},
		{"Invalid", Invalid, 0xFF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if byte(tt.pt) != tt.want {
				t.Errorf("got %d, want %d", byte(tt.pt), tt.want)
			}
		})
	}
}

func TestIsBigPacket(t *testing.T) {
	big := []PackageType{Hello, Awake, Heartbeat, HeartbeatEx, Handshake, HandshakeAck}
	for _, pt := range big {
		if !IsBigPacket(pt) {
			t.Errorf("%d should be big packet", pt)
		}
	}
	small := []PackageType{Mouse, Keyboard, ByeBye, HideMouse}
	for _, pt := range small {
		if IsBigPacket(pt) {
			t.Errorf("%d should be small packet", pt)
		}
	}
}
