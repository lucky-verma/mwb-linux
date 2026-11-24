package network

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

type failedReader struct{ err error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.err }

func receiveTestConn(t *testing.T, packets []*protocol.Packet, tail []byte, readErr error) *Conn {
	t.Helper()
	key, iv := make([]byte, 32), make([]byte, 16)
	const magic uint32 = 0x12340000
	var wire bytes.Buffer
	writer, err := protocol.NewEncryptWriter(&wire, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkt := range packets {
		buf := pkt.Marshal()
		protocol.StampPacket(buf, magic)
		if _, err := writer.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Write(tail); err != nil {
		t.Fatal(err)
	}
	var reader io.Reader = &wire
	if readErr != nil {
		reader = io.MultiReader(reader, failedReader{readErr})
	}
	dec, err := protocol.NewDecryptReader(reader, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := protocol.NewEncryptWriter(io.Discard, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	return &Conn{dec: dec, enc: enc, magic: magic}
}

func TestReceiveLoopReleasesInput(t *testing.T) {
	readErr := errors.New("connection reset")
	for _, tt := range []struct {
		name    string
		bye     bool
		tail    []byte
		readErr error
		wantErr bool
	}{
		{name: "EOF"},
		{name: "ByeBye", bye: true},
		{name: "truncated packet", tail: make([]byte, 16)},
		{name: "invalid packet", tail: make([]byte, protocol.PacketSize), wantErr: true},
		{name: "read error", readErr: readErr, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockInputDevice{}
			h := &Handler{Mouse: mock, Keyboard: mock}
			packets := []*protocol.Packet{
				keyboardPacket(0x5B, 0),
				mouseButtonPacket(protocol.WM_LBUTTONDOWN, 0),
				keyboardPacket(0xA2, 0), // Must be discarded, not flushed on exit.
			}
			if tt.bye {
				packets = append(packets, &protocol.Packet{Type: protocol.ByeBye})
			}
			err := ReceiveLoop(receiveTestConn(t, packets, tt.tail, tt.readErr), h)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReceiveLoop error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.readErr != nil && !errors.Is(err, tt.readErr) {
				t.Errorf("receive error was lost: %v", err)
			}
			if want := []uint16{input.KEY_LEFTMETA}; !reflect.DeepEqual(mock.KeyUps, want) {
				t.Errorf("KeyUps = %v, want %v", mock.KeyUps, want)
			}
			if want := []uint16{input.BTN_LEFT}; !reflect.DeepEqual(mock.ButtonUps, want) {
				t.Errorf("ButtonUps = %v, want %v", mock.ButtonUps, want)
			}
			if want := []uint16{input.KEY_LEFTMETA}; !reflect.DeepEqual(mock.KeyDowns, want) {
				t.Errorf("KeyDowns = %v, want %v (no Ctrl flush)", mock.KeyDowns, want)
			}

			// Reuse the same handler and devices, as main's reconnect loop does.
			next := []*protocol.Packet{keyboardPacket(0x4F, 0), keyboardPacket(0x4F, protocol.LLKHF_UP)}
			if err := ReceiveLoop(receiveTestConn(t, next, nil, nil), h); err != nil {
				t.Fatal(err)
			}
			if want := []uint16{input.KEY_LEFTMETA, input.KEY_O}; !reflect.DeepEqual(mock.KeyDowns, want) {
				t.Errorf("reconnected KeyDowns = %v, want %v", mock.KeyDowns, want)
			}
			if want := []uint16{input.KEY_LEFTMETA, input.KEY_O}; !reflect.DeepEqual(mock.KeyUps, want) {
				t.Errorf("reconnected KeyUps = %v, want %v", mock.KeyUps, want)
			}
		})
	}
}

func TestReceiveLoopReportsCleanupErrors(t *testing.T) {
	releaseErr := errors.New("release failed")
	readErr := errors.New("connection reset")
	for _, terminalErr := range []error{nil, readErr} {
		mock := &failingInputDevice{upErr: releaseErr}
		h := &Handler{Mouse: mock, Keyboard: mock}
		packets := []*protocol.Packet{
			keyboardPacket(0x5B, 0),
			mouseButtonPacket(protocol.WM_LBUTTONDOWN, 0),
		}
		err := ReceiveLoop(receiveTestConn(t, packets, nil, terminalErr), h)
		if !errors.Is(err, releaseErr) || !strings.Contains(err.Error(), "release input on disconnect") {
			t.Errorf("cleanup error was lost: %v", err)
		}
		if terminalErr != nil && !errors.Is(err, terminalErr) {
			t.Errorf("receive error was lost: %v", err)
		}
		if len(mock.KeyUps) != 1 || len(mock.ButtonUps) != 1 {
			t.Errorf("not all releases attempted: keys=%v, buttons=%v", mock.KeyUps, mock.ButtonUps)
		}
	}
}
