## What

<!-- One paragraph: what changed and why. -->

## Test plan

<!-- Check every item that applies. If an item is N/A, strike it out with ~~text~~. -->

### Automated
- [ ] `make build` passes
- [ ] `go test -race ./...` — all tests pass, no races detected
- [ ] `golangci-lint run` — 0 issues

### Manual (bidirectional mode)
- [ ] Cursor crosses to Windows and stays there (doesn't bounce back immediately)
- [ ] Cursor returns to Ubuntu from Windows right edge
- [ ] After return: Ubuntu keyboard and mouse work immediately
- [ ] After Windows screen lock + unlock: cursor recovers without restart
- [ ] Clipboard text: copy on Ubuntu, paste on Windows (and reverse)

### Input isolation safety (required for any capture/ change)
- [ ] After switch → return: local mouse and keyboard both work again
- [ ] `kill -9` the process mid-switch, while the cursor is on the remote: local input must come back on its own (the kernel drops EVIOCGRAB when the fd closes)
- [ ] Log line on switch reads `grabbed local input devices count=N of=N` — a lower count means a device leaked to the local display
- [ ] The physical power button still works while the cursor is on the remote
- [ ] Remote typing and clicking still land locally (mwb's own uinput devices must never be grabbed)

## Checklist

- [ ] Public text and metadata do not include private hostnames, usernames, machine names, internal paths, private domains, or personal email addresses
- [ ] Package/release metadata uses an intentional public contact address
- [ ] No mutex held when calling a method that also acquires that mutex
- [ ] `isGrabTarget` classifies by capability bitmask, never by vendor or product name
- [ ] Virtual (uinput) devices are excluded from grabbing
- [ ] New `/dev/input` device nodes are opened with `O_NONBLOCK` so `Close()` can interrupt a pending read
- [ ] `OnActivated` and `OnReclaimed` both move cursor away from edge after switch
- [ ] Any new goroutine is tracked in a `WaitGroup` and has a stop path
- [ ] Any new `exec.Command` has a `context.WithTimeout`
