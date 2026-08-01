package network

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
)

const testKey = "TestSecurityKey!!"

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func staticMachineID(id uint32) func() uint32 {
	return func() uint32 { return id }
}

// The file channel uses the base/clipboard port. This drives the whole inbound
// branch end to end: dial, IV exchange, raw Clipboard header, file header,
// body, and the write to disk.
func TestFileChannel_EndToEnd(t *testing.T) {
	port := freePort(t)
	dir := t.TempDir()

	var wg sync.WaitGroup
	wg.Add(1)
	var gotPath string
	var gotErr error

	stop := make(chan struct{})
	onFile := func(c *Conn, push bool) {
		defer wg.Done()
		res, err := filetransfer.Receive(c.Reader(), dir, filetransfer.DefaultMaxSize)
		if err != nil {
			gotErr = err
			return
		}
		gotPath = res.Path
	}

	if err := ListenFileChannel(port, testKey, "listener", "127.0.0.1", staticMachineID(77), onFile, stop); err != nil {
		t.Fatal(err)
	}
	defer close(stop)
	time.Sleep(200 * time.Millisecond) // let the listener bind

	src := filepath.Join(t.TempDir(), "transferred.bin")
	body := bytes.Repeat([]byte("payload"), 3000)
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}

	conn, err := DialFile(fmt.Sprintf("127.0.0.1:%d", port), testKey, "sender", 42, 5*time.Second)
	if err != nil {
		t.Fatalf("DialFile: %v", err)
	}
	if err := filetransfer.Send(conn.Writer(), src, filetransfer.DefaultMaxSize); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_ = conn.raw.Close()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("receiver never completed; the inbound branch did not route the file connection")
	}

	if gotErr != nil {
		t.Fatalf("receive failed: %v", gotErr)
	}
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("received %d bytes, sent %d", len(got), len(body))
	}
	if filepath.Base(gotPath) != "transferred.bin" {
		t.Errorf("landed as %q, want transferred.bin", filepath.Base(gotPath))
	}
}

// Reading the first packet before responding must not break control
// connections, which is the regression that would silently kill every inbound
// handshake.
func TestFileChannel_ControlHandshakeStillWorks(t *testing.T) {
	port := freePort(t)
	stop := make(chan struct{})

	connCh, err := ListenAndAccept(port, testKey, "listener", "127.0.0.1", nil, stop)
	if err != nil {
		t.Fatal(err)
	}
	defer close(stop)
	time.Sleep(200 * time.Millisecond)

	client, err := Connect(fmt.Sprintf("127.0.0.1:%d", port), testKey, "sender", 5*time.Second)
	if err != nil {
		t.Fatalf("control connect failed after the inbound branch was added: %v", err)
	}
	defer func() { _ = client.Close() }()

	select {
	case accepted := <-connCh:
		if accepted == nil {
			t.Fatal("listener closed without delivering the control connection")
		}
		defer func() { _ = accepted.Close() }()
		if accepted.RemoteName != "sender" {
			t.Errorf("RemoteName = %q, want sender", accepted.RemoteName)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("control connection never reached the channel")
	}
}

// With no handler registered the previous behaviour must hold: file
// connections are closed rather than served.
func TestFileChannel_NilHandlerRejects(t *testing.T) {
	port := freePort(t)
	stop := make(chan struct{})

	if _, err := ListenAndAccept(port, testKey, "listener", "127.0.0.1", nil, stop); err != nil {
		t.Fatal(err)
	}
	defer close(stop)
	time.Sleep(200 * time.Millisecond)

	conn, err := DialFile(fmt.Sprintf("127.0.0.1:%d", port), testKey, "sender", 42, 5*time.Second)
	if err != nil {
		return // refused during setup is an acceptable rejection
	}
	defer func() { _ = conn.Close() }()

	// The listener should close the connection; a write plus read must fail.
	_ = conn.raw.SetDeadline(time.Now().Add(3 * time.Second))
	hdr, err := filetransfer.EncodeHeader(filetransfer.Header{Size: 4, Name: "x.bin"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Writer().Write(hdr)
	buf := make([]byte, 1)
	if _, err := conn.raw.Read(buf); err == nil {
		t.Error("connection stayed open with no file handler registered")
	}
}
