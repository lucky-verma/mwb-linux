package filetransfer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- header framing ---

func TestHeaderRoundTrip(t *testing.T) {
	for _, name := range []string{
		"report.pdf",
		"file with spaces.txt",
		"ünïcödé-名前.png",
		"star*in*name.txt", // legal on Linux; only the first separator counts
	} {
		buf, err := EncodeHeader(Header{Size: 4096, Name: name})
		if err != nil {
			t.Fatalf("encode %q: %v", name, err)
		}
		if len(buf) != HeaderSize {
			t.Fatalf("header is %d bytes, want %d", len(buf), HeaderSize)
		}
		got, err := ParseHeader(buf, DefaultMaxSize)
		if err != nil {
			t.Fatalf("parse %q: %v", name, err)
		}
		if got.Size != 4096 || got.Name != name {
			t.Errorf("round trip = %+v, want size=4096 name=%q", got, name)
		}
	}
}

func TestParseHeader_Rejects(t *testing.T) {
	hdr := func(text string) []byte {
		buf := make([]byte, HeaderSize)
		for i, r := range []rune(text) {
			buf[i*2] = byte(r)
			buf[i*2+1] = byte(r >> 8)
		}
		return buf
	}

	for _, tc := range []struct {
		name string
		buf  []byte
		want error
	}{
		{"truncated", make([]byte, 512), ErrHeaderTooShort},
		{"no separator", hdr("1234"), ErrHeaderMalformed},
		{"non-numeric size", hdr("abc*file.txt"), ErrHeaderMalformed},
		{"empty name", hdr("100*"), ErrHeaderMalformed},
		{"negative size", hdr("-1*file.txt"), ErrSizeRejected},
		{"over cap", hdr("999999999999*file.txt"), ErrSizeRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseHeader(tc.buf, DefaultMaxSize); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A peer must not be able to declare a size that overflows into acceptance.
func TestParseHeader_HonoursCap(t *testing.T) {
	buf, err := EncodeHeader(Header{Size: 2048, Name: "x.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHeader(buf, 1024); !errors.Is(err, ErrSizeRejected) {
		t.Errorf("a 2048 byte file under a 1024 byte cap must be refused, got %v", err)
	}
	if _, err := ParseHeader(buf, 4096); err != nil {
		t.Errorf("a 2048 byte file under a 4096 byte cap must be accepted, got %v", err)
	}
}

func TestIsInlinePayload(t *testing.T) {
	for name, want := range map[string]bool{
		"text":            true,
		"textDATA":        true,
		"image.png":       true,
		"IMAGE_clip":      true,
		"report.pdf":      false,
		"my-text-file.md": false, // only a prefix counts
	} {
		if got := IsInlinePayload(name); got != want {
			t.Errorf("IsInlinePayload(%q) = %v, want %v", name, got, want)
		}
	}
}

// --- filename safety ---

// The name comes from a remote machine. Every one of these is a real escape
// attempt, and the Windows-style ones matter most: filepath on Linux does not
// treat "\" as a separator, so an unnormalised basename call would create a
// file literally named "..\..\etc\passwd" instead of rejecting it.
func TestSafeName_ContainsEscapes(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"report.pdf", "report.pdf"},
		{"/etc/passwd", "passwd"},
		{"../../etc/passwd", "passwd"},
		{`..\..\Windows\System32\evil.dll`, "evil.dll"},
		{`C:\Users\me\Desktop\notes.txt`, "notes.txt"},
		{"subdir/file.txt", "file.txt"},
		{"....//....//escape.sh", "escape.sh"},
		{".bashrc", "_.bashrc"}, // must not silently land as a dotfile
	} {
		got, err := SafeName(tc.raw)
		if err != nil {
			t.Errorf("SafeName(%q) errored: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("SafeName(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("SafeName(%q) = %q still contains a separator", tc.raw, got)
		}
	}
}

func TestSafeName_RejectsUnusable(t *testing.T) {
	for _, raw := range []string{"", ".", "..", "/", "///", "../..", "\x00"} {
		if got, err := SafeName(raw); err == nil {
			t.Errorf("SafeName(%q) = %q, want an error", raw, got)
		}
	}
}

// --- destination selection ---

func TestDestPath_NeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DestPath(dir, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "a (1).txt" {
		t.Errorf("collision produced %q, want %q", filepath.Base(got), "a (1).txt")
	}

	// The original must be untouched.
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "original" {
		t.Errorf("original file was modified: %q %v", body, err)
	}
}

// A symlink planted in the destination must not redirect the write. Lstat, not
// Stat, is what makes a dangling link count as occupied.
func TestDestPath_TreatsSymlinkAsOccupied(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.Symlink(target, filepath.Join(dir, "payload.bin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := DestPath(dir, "payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) == "payload.bin" {
		t.Error("a dangling symlink was treated as free; the write would follow it to its target")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("symlink target was created")
	}
}

func TestDestPath_StaysInsideDir(t *testing.T) {
	dir := t.TempDir()
	got, err := DestPath(dir, "ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != real {
		t.Errorf("destination %q is not inside %q", got, real)
	}
}

// --- streaming ---

// wire builds what a real sender puts on the stream: the fixed header followed
// by a block-aligned body, since the transport is raw AES-CBC and only ever
// carries whole blocks.
func wire(t *testing.T, h Header, body []byte) []byte {
	t.Helper()
	hdr, err := EncodeHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	var padded bytes.Buffer
	if _, err := writeAligned(&padded, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	return append(hdr, padded.Bytes()...)
}

func TestReceive_WritesFile(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello from the other machine")
	in := bytes.NewReader(wire(t, Header{Size: int64(len(body)), Name: "note.txt"}, body))

	res, err := Receive(in, dir, DefaultMaxSize)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("content = %q, want %q", got, body)
	}

	// Never executable: the sender is remote.
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Errorf("mode = %v, must not be executable", info.Mode().Perm())
	}
}

func TestReceive_RejectsPowerToysStatusHeaders(t *testing.T) {
	for _, name := range []string{
		`C:\Downloads\large.exe - File too big (greater than 100MB), please drag and drop the file instead!`,
		`C:\Users\me\Documents - Folder is not supported, zip it first!`,
		`C:\Downloads\vanished.txt not found!`,
	} {
		t.Run(filepath.Base(name), func(t *testing.T) {
			dir := t.TempDir()
			in := bytes.NewReader(wire(t, Header{Size: 0, Name: name}, nil))

			if _, err := Receive(in, dir, DefaultMaxSize); !errors.Is(err, ErrPeerRejected) {
				t.Fatalf("err = %v, want ErrPeerRejected", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("peer error created %d filesystem entries", len(entries))
			}
		})
	}
}

func TestReceive_AllowsLegitimateEmptyFile(t *testing.T) {
	dir := t.TempDir()
	in := bytes.NewReader(wire(t, Header{Size: 0, Name: "empty.txt"}, nil))

	res, err := Receive(in, dir, DefaultMaxSize)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("empty file size = %d, want 0", info.Size())
	}
}

// A peer that declares one size and sends more must not write past the cap.
func TestReceive_UnderDeclaredSizeCannotOverrun(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("A"), 4096)
	in := bytes.NewReader(wire(t, Header{Size: 10, Name: "lie.bin"}, body))

	res, err := Receive(in, dir, DefaultMaxSize)
	if err != nil {
		t.Fatal(err)
	}
	if res.Size != 10 {
		t.Errorf("wrote %d bytes, want exactly the declared 10", res.Size)
	}
}

// A truncated transfer must leave nothing that looks complete.
func TestReceive_ShortBodyLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	in := bytes.NewReader(wire(t, Header{Size: 1000, Name: "cut.bin"}, []byte("only a few")))

	if _, err := Receive(in, dir, DefaultMaxSize); !errors.Is(err, ErrShortBody) {
		t.Fatalf("err = %v, want ErrShortBody", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind %q; an aborted transfer must clean up", e.Name())
	}
}

func TestReceive_RejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	body := []byte("pwned")
	in := bytes.NewReader(wire(t, Header{Size: int64(len(body)), Name: `..\..\..\etc\cron.d\evil`}, body))

	res, err := Receive(in, dir, DefaultMaxSize)
	if err != nil {
		t.Fatalf("unexpected error: %v", err) // basename reduction should make it safe
	}
	if filepath.Dir(res.Path) != mustEval(t, dir) {
		t.Errorf("wrote to %q, outside %q", res.Path, dir)
	}
	if filepath.Base(res.Path) != "evil" {
		t.Errorf("name = %q, want the basename only", filepath.Base(res.Path))
	}
}

func TestReceive_InlinePayloadStaysInMemory(t *testing.T) {
	dir := t.TempDir()
	body := []byte("clipboard text content")
	in := bytes.NewReader(wire(t, Header{Size: int64(len(body)), Name: "text"}, body))

	res, err := Receive(in, dir, DefaultMaxSize)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "" {
		t.Errorf("inline payload was written to %q; it belongs in memory", res.Path)
	}
	if !bytes.Equal(res.Inline, body) {
		t.Errorf("inline = %q, want %q", res.Inline, body)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("inline payload created %d files on disk", len(entries))
	}
}

func TestSend_RoundTripsThroughReceive(t *testing.T) {
	src := filepath.Join(t.TempDir(), "payload.bin")
	body := bytes.Repeat([]byte("xyz"), 5000)
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Send(&buf, src, DefaultMaxSize); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	res, err := Receive(&buf, dir, DefaultMaxSize)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("round trip corrupted %d bytes", len(body))
	}
	if filepath.Base(res.Path) != "payload.bin" {
		t.Errorf("name = %q, want payload.bin", filepath.Base(res.Path))
	}
}

func TestSend_RefusesDirectoriesAndOversize(t *testing.T) {
	dir := t.TempDir()
	if err := Send(&bytes.Buffer{}, dir, DefaultMaxSize); !errors.Is(err, ErrUnsafeName) {
		t.Errorf("sending a directory: err = %v, want ErrUnsafeName", err)
	}

	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte("z"), 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Send(&bytes.Buffer{}, big, 1024); !errors.Is(err, ErrSizeRejected) {
		t.Errorf("sending over the cap: err = %v, want ErrSizeRejected", err)
	}
}

// Sendable is the gate a caller runs before opening a connection, so its
// verdict has to match what Send would decide on its own.
func TestSendable_MatchesWhatSendAccepts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ok.bin")
	if err := os.WriteFile(file, bytes.Repeat([]byte("k"), 300), 0o644); err != nil {
		t.Fatal(err)
	}

	// An unset cap must resolve to MWB's own limit rather than rejecting
	// everything as "over a zero-byte cap".
	size, err := Sendable(file, 0)
	if err != nil {
		t.Fatalf("Sendable with an unset cap: %v", err)
	}
	if size != 300 {
		t.Errorf("size = %d, want 300", size)
	}

	if _, err := Sendable(dir, DefaultMaxSize); !errors.Is(err, ErrUnsafeName) {
		t.Errorf("directory: err = %v, want ErrUnsafeName", err)
	}
	if _, err := Sendable(file, 16); !errors.Is(err, ErrSizeRejected) {
		t.Errorf("over the cap: err = %v, want ErrSizeRejected", err)
	}
	if _, err := Sendable(filepath.Join(dir, "absent.bin"), DefaultMaxSize); err == nil {
		t.Error("a missing path was accepted")
	}
}

func mustEval(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// Allocation must follow the bytes that actually arrive, not the header. Sizing
// the inline buffer from the declared size let one connection reserve the whole
// cap, multiplied by however many connections the accept path allows.
func TestReceive_InlineAllocationFollowsRealData(t *testing.T) {
	dir := t.TempDir()
	body := []byte("tiny")
	in := bytes.NewReader(wire(t, Header{Size: int64(len(body)), Name: "text"}, body))

	res, err := Receive(in, dir, 64<<20) // a generous cap must not be pre-reserved
	if err != nil {
		t.Fatal(err)
	}
	if got := cap(res.Inline); int64(got) >= 64<<20 {
		t.Errorf("allocated %d bytes of capacity for a %d byte payload", got, len(body))
	}
	if !bytes.Equal(res.Inline, body) {
		t.Errorf("payload = %q, want %q", res.Inline, body)
	}
}

// A peer that declares a large size and then stops must be refused, not
// buffered while we wait for bytes that never come.
func TestReceive_TruncatedInlineIsShortBody(t *testing.T) {
	dir := t.TempDir()
	hdr, err := EncodeHeader(Header{Size: 64 << 20, Name: "text"})
	if err != nil {
		t.Fatal(err)
	}
	in := bytes.NewReader(append(hdr, make([]byte, blockSize)...))

	if _, err := Receive(in, dir, DefaultMaxSize); !errors.Is(err, ErrShortBody) {
		t.Errorf("err = %v, want ErrShortBody", err)
	}
}

// An inline payload larger than the cap must be refused rather than buffered.
func TestReceive_InlineHonoursCap(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("x"), 4096)
	in := bytes.NewReader(wire(t, Header{Size: int64(len(body)), Name: "text"}, body))

	if _, err := Receive(in, dir, 1024); err == nil {
		t.Error("a 4096 byte inline payload under a 1024 byte cap must be refused")
	}
}

// The destination must exist before free space is measured, or statfs fails on
// a missing path and the check passes by default on first use.
func TestReceive_CreatesDestinationDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "does", "not", "exist", "yet")
	body := []byte("content")
	in := bytes.NewReader(wire(t, Header{Size: int64(len(body)), Name: "f.txt"}, body))

	res, err := Receive(in, dir, DefaultMaxSize)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

// --- block alignment ---

// The transport is raw AES-CBC: EncryptWriter refuses a partial block and
// DecryptReader consumes a whole one regardless of the caller's buffer. Every
// size that is not a multiple of 16 exercises the padding path, and getting it
// wrong truncates the tail of the file.
func TestAlignedRoundTrip_AllTailLengths(t *testing.T) {
	for size := 0; size <= 64; size++ {
		body := bytes.Repeat([]byte{0xAB}, size)

		var wire bytes.Buffer
		sent, err := writeAligned(&wire, bytes.NewReader(body), int64(size))
		if err != nil {
			t.Fatalf("size %d: writeAligned: %v", size, err)
		}
		if sent != int64(size) {
			t.Fatalf("size %d: sent %d", size, sent)
		}
		if wire.Len()%blockSize != 0 {
			t.Fatalf("size %d: wire is %d bytes, not block aligned", size, wire.Len())
		}

		var out bytes.Buffer
		got, err := readAligned(&out, &wire, int64(size))
		if err != nil {
			t.Fatalf("size %d: readAligned: %v", size, err)
		}
		if got != int64(size) || !bytes.Equal(out.Bytes(), body) {
			t.Errorf("size %d: round trip gave %d bytes %q", size, got, out.Bytes())
		}
	}
}

// Padding must be zeros, not leftover buffer contents from a previous chunk.
func TestWriteAligned_PadsWithZeros(t *testing.T) {
	var wire bytes.Buffer
	if _, err := writeAligned(&wire, bytes.NewReader([]byte("abc")), 3); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != blockSize {
		t.Fatalf("wire = %d bytes, want %d", wire.Len(), blockSize)
	}
	if !bytes.Equal(wire.Bytes()[3:], make([]byte, blockSize-3)) {
		t.Errorf("padding = %v, want zeros", wire.Bytes()[3:])
	}
}

// Sizes spanning multiple buffer fills must stay aligned throughout.
func TestAlignedRoundTrip_LargeUnaligned(t *testing.T) {
	const size = copyBufSize*2 + 7
	body := bytes.Repeat([]byte("Q"), size)

	var wire bytes.Buffer
	if _, err := writeAligned(&wire, bytes.NewReader(body), size); err != nil {
		t.Fatal(err)
	}
	if wire.Len()%blockSize != 0 {
		t.Fatalf("wire is %d bytes, not block aligned", wire.Len())
	}

	var out bytes.Buffer
	if _, err := readAligned(&out, &wire, size); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), body) {
		t.Errorf("round trip corrupted %d bytes", size)
	}
}

func TestAlignedLen(t *testing.T) {
	for in, want := range map[int64]int64{0: 0, 1: 16, 15: 16, 16: 16, 17: 32, 1024: 1024, 1025: 1040} {
		if got := alignedLen(in); got != want {
			t.Errorf("alignedLen(%d) = %d, want %d", in, got, want)
		}
	}
}
