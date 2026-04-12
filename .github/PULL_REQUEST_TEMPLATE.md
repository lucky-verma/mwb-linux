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

### xinput safety (required for any capture/ change)
- [ ] Wooting devices show `[slave pointer/keyboard]` (not `[floating slave]`) after restart
- [ ] Razer devices show `[slave pointer/keyboard]` (not `[floating slave]`) after restart
- [ ] After switch → return: `xinput list-props <id> | grep "Device Enabled"` shows `1` for all Wooting/Razer

## Checklist

- [ ] No mutex held when calling a method that also acquires that mutex
- [ ] `enableXinput()` only called when `disabledXinputIDs` is non-empty OR as cleanup
- [ ] `parseXinputIDs` / `getXinputIDs` excludes `[floating slave]` devices
- [ ] `OnActivated` and `OnReclaimed` both move cursor away from edge after switch
- [ ] Any new goroutine is tracked in a `WaitGroup` and has a stop path
- [ ] Any new `exec.Command` has a `context.WithTimeout`
