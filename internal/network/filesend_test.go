package network

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// deadAddr returns an address with nothing listening on it. Passing it to a
// FileSender turns "did this dial?" into an assertion: a dial attempt fails
// loudly, so a nil or validation error proves the send was rejected first.
func deadAddr(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}

// receivingListener starts a listener that writes inbound file channels into a
// temp dir, and returns its address plus a function that waits for one file and
// reports where it landed.
func receivingListener(t *testing.T) (addr string, wait func() string) {
	t.Helper()
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
	t.Cleanup(func() { close(stop) })
	time.Sleep(200 * time.Millisecond) // let the listener bind

	return fmt.Sprintf("127.0.0.1:%d", port), func() string {
		t.Helper()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("receiver never completed")
		}
		if gotErr != nil {
			t.Fatalf("receive failed: %v", gotErr)
		}
		return gotPath
	}
}

func writeTemp(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The outbound half end to end: clipboard selection, dial, channel header, file
// header, body, and the peer's write to disk.
func TestFileSender_SendsSingleFile(t *testing.T) {
	addr, wait := receivingListener(t)

	body := bytes.Repeat([]byte("outbound"), 4000)
	src := writeTemp(t, "sent-from-linux.bin", body)

	sender := FileSender{
		Addr:        addr,
		SecurityKey: testKey,
		MachineName: "sender",
		MachineID:   42,
		DialTimeout: 5 * time.Second,
	}
	if err := sender.Send([]string{src}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	landed := wait()
	got, err := os.ReadFile(landed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("received %d bytes, sent %d", len(got), len(body))
	}
	if filepath.Base(landed) != "sent-from-linux.bin" {
		t.Errorf("landed as %q, want sent-from-linux.bin", filepath.Base(landed))
	}
}

// A file whose length is not a whole number of AES blocks is the case that
// silently loses its tail if the padding is mishandled on either side.
func TestFileSender_SendsUnalignedFile(t *testing.T) {
	addr, wait := receivingListener(t)

	body := bytes.Repeat([]byte("x"), 4097) // 256 blocks + 1 byte
	src := writeTemp(t, "unaligned.bin", body)

	sender := FileSender{
		Addr: addr, SecurityKey: testKey, MachineName: "sender",
		MachineID: 42, DialTimeout: 5 * time.Second,
	}
	if err := sender.Send([]string{src}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := os.ReadFile(wait())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("received %d bytes, sent %d", len(got), len(body))
	}
}

// Copying several files at once is normal and has no protocol representation,
// so it is skipped rather than reduced to an arbitrary member of the selection.
func TestFileSender_SkipsMultiFileSelection(t *testing.T) {
	sender := FileSender{
		Addr: deadAddr(t), SecurityKey: testKey, MachineName: "sender",
		MachineID: 42, DialTimeout: time.Second,
	}
	err := sender.Send([]string{
		writeTemp(t, "a.txt", []byte("a")),
		writeTemp(t, "b.txt", []byte("b")),
	})
	if err != nil {
		t.Fatalf("multi-file selection should be skipped, not attempted: %v", err)
	}
}

func TestFileSender_EmptySelectionIsNoop(t *testing.T) {
	sender := FileSender{
		Addr: deadAddr(t), SecurityKey: testKey, MachineName: "sender",
		MachineID: 42, DialTimeout: time.Second,
	}
	if err := sender.Send(nil); err != nil {
		t.Fatalf("empty selection should not dial: %v", err)
	}
}

// Folder copies are frequent. Rejecting one after dialing would leave the peer
// a file channel it only ever sees abandoned, so the check must come first —
// which is what reaching ErrUnsafeName instead of a dial failure proves.
func TestFileSender_RejectsDirectoryBeforeDialing(t *testing.T) {
	sender := FileSender{
		Addr: deadAddr(t), SecurityKey: testKey, MachineName: "sender",
		MachineID: 42, DialTimeout: time.Second,
	}
	err := sender.Send([]string{t.TempDir()})
	if !errors.Is(err, filetransfer.ErrUnsafeName) {
		t.Fatalf("err = %v, want ErrUnsafeName before any dial", err)
	}
}

func TestFileSender_RejectsOversizeBeforeDialing(t *testing.T) {
	sender := FileSender{
		Addr: deadAddr(t), SecurityKey: testKey, MachineName: "sender",
		MachineID: 42, MaxSize: 16, DialTimeout: time.Second,
	}
	err := sender.Send([]string{writeTemp(t, "big.bin", bytes.Repeat([]byte("z"), 1024))})
	if !errors.Is(err, filetransfer.ErrSizeRejected) {
		t.Fatalf("err = %v, want ErrSizeRejected before any dial", err)
	}
}

// The first packet declares direction, not merely "this is a file channel".
// MWB reads it as clientPushData: ClipboardPush means the dialer sends,
// Clipboard means the dialer is asking for the peer's clipboard and the peer
// sends. Announcing the wrong one leaves both ends waiting to read, and it
// fails silently — the sender writes its file into the socket and logs success
// while nothing ever arrives.
func TestFileSender_AnnouncesAPush(t *testing.T) {
	port := freePort(t)
	dir := t.TempDir()
	gotPush := make(chan bool, 1)
	receiveDone := make(chan error, 1)

	stop := make(chan struct{})
	onFile := func(c *Conn, push bool) {
		gotPush <- push
		_, err := filetransfer.Receive(c.Reader(), dir, filetransfer.DefaultMaxSize)
		receiveDone <- err
	}
	if err := ListenFileChannel(port, testKey, "listener", "127.0.0.1", staticMachineID(77), onFile, stop); err != nil {
		t.Fatal(err)
	}
	defer close(stop)
	time.Sleep(200 * time.Millisecond)

	sender := FileSender{
		Addr: fmt.Sprintf("127.0.0.1:%d", port), SecurityKey: testKey,
		MachineName: "sender", MachineID: 42, DialTimeout: 5 * time.Second,
	}
	if err := sender.Send([]string{writeTemp(t, "push.bin", []byte("payload"))}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case push := <-gotPush:
		if !push {
			t.Error("channel announced Clipboard, a pull; the peer will wait for us to read instead of reading")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("file channel never reached the handler")
	}

	select {
	case err := <-receiveDone:
		if err != nil {
			t.Fatalf("receive failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("file channel handler never completed")
	}
}

// MWB's ShakeHand writes a header and then reads the peer's, in both roles.
// Skipping either half leaves the other end sitting in ReadEx until its 30s
// receive timeout, which it reports as a rejected channel rather than a failed
// transfer — so the symptom is a silent nothing on both sides.
func TestFileChannel_HandshakeIsExchangedBothWays(t *testing.T) {
	port := freePort(t)
	dir := t.TempDir()

	stop := make(chan struct{})
	onFile := func(c *Conn, push bool) {
		_, _ = filetransfer.Receive(c.Reader(), dir, filetransfer.DefaultMaxSize)
	}
	if err := ListenFileChannel(port, testKey, "listener", "127.0.0.1", staticMachineID(77), onFile, stop); err != nil {
		t.Fatal(err)
	}
	defer close(stop)
	time.Sleep(200 * time.Millisecond)

	conn, err := DialFile(fmt.Sprintf("127.0.0.1:%d", port), testKey, "sender", 42, 5*time.Second)
	if err != nil {
		t.Fatalf("DialFile: %v", err)
	}
	defer func() { _ = conn.raw.Close() }()

	if conn.RemoteName != "listener" {
		t.Errorf("RemoteName = %q, want listener; the peer's header was never read", conn.RemoteName)
	}
	if conn.RemoteID != 77 {
		t.Errorf("RemoteID = %d, want active control ID 77; a per-file random ID is rejected by PowerToys", conn.RemoteID)
	}
}

// Clipboard.ShakeHand writes package.Bytes directly. That means the first
// uint32 must contain only PackageType and must not have the magic/checksum
// stamp used by the control stream. A stamped header looks like an unknown
// enum value to PowerToys and was the reason Linux logged success while
// Windows discarded every transfer.
func TestDialFile_SendsRawPasteableChannelHeader(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type observed struct {
		raw []byte
		pkt *protocol.Packet
		err error
	}
	seen := make(chan observed, 1)
	go func() {
		raw, acceptErr := ln.Accept()
		if acceptErr != nil {
			seen <- observed{err: acceptErr}
			return
		}
		defer func() { _ = raw.Close() }()

		peer, setupErr := newSecureStream(raw, testKey, "windows", 5*time.Second)
		if setupErr != nil {
			seen <- observed{err: setupErr}
			return
		}
		buf := make([]byte, protocol.PacketSizeEx)
		if _, readErr := io.ReadFull(peer.dec, buf); readErr != nil {
			seen <- observed{err: readErr}
			return
		}
		pkt, parseErr := protocol.UnmarshalPacket(buf)
		if parseErr == nil {
			ack := &protocol.Packet{Type: protocol.ClipboardPush, Src: 77}
			ack.SetMachineName("windows")
			parseErr = peer.sendChannelHeader(ack)
		}
		seen <- observed{raw: buf, pkt: pkt, err: parseErr}
	}()

	conn, err := DialFile(ln.Addr().String(), testKey, "linux", 42, 5*time.Second)
	if err != nil {
		t.Fatalf("DialFile: %v", err)
	}
	_ = conn.raw.Close()

	got := <-seen
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !bytes.Equal(got.raw[1:4], []byte{0, 0, 0}) {
		t.Fatalf("header bytes 1-3 = %x, want unstamped 000000", got.raw[1:4])
	}
	if got.pkt.Type != protocol.ClipboardPush {
		t.Errorf("Type = %d, want ClipboardPush", got.pkt.Type)
	}
	if got.pkt.Src != 42 {
		t.Errorf("Src = %d, want control-channel machine ID 42", got.pkt.Src)
	}
	if got.pkt.MachineName() != "linux" {
		t.Errorf("MachineName = %q, want linux", got.pkt.MachineName())
	}
	if got.pkt.PostAction != protocol.PostActionOther {
		t.Errorf("PostAction = %d, want Other so the file can be pasted into any folder", got.pkt.PostAction)
	}
}

// The clipboard port is where MWB actually opens file channels. Serving only
// the control port means a pushing peer is never answered, which is how this
// direction failed silently for a whole release.
func TestListenFileChannel_ReceivesAPush(t *testing.T) {
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
	time.Sleep(200 * time.Millisecond)

	body := bytes.Repeat([]byte("inbound"), 500)
	sender := FileSender{
		Addr: fmt.Sprintf("127.0.0.1:%d", port), SecurityKey: testKey,
		MachineName: "sender", MachineID: 42, DialTimeout: 5 * time.Second,
	}
	if err := sender.Send([]string{writeTemp(t, "pushed.bin", body)}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the file listener never completed a transfer")
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
}

// A peer that stops reading must not pin the sending goroutine: clipboard
// shutdown waits on it, and the reconnect loop waits on clipboard shutdown.
func TestIdleWriter_BoundsAStalledPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c // deliberately never read from
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	peer, ok := <-accepted
	if !ok {
		t.Fatal("listener never accepted")
	}
	defer func() { _ = peer.Close() }()

	// Larger than both socket buffers, so the write cannot complete without the
	// peer reading and must sit until the deadline fires.
	iw := &idleWriter{conn: c, w: c, timeout: 200 * time.Millisecond}
	start := time.Now()
	if _, err = iw.Write(make([]byte, 32*1024*1024)); err == nil {
		t.Fatal("write to a peer that never reads returned success")
	}

	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("write took %v to give up; the deadline is not being applied", elapsed)
	}
}
