# File transfer design

Status: implemented
Date: 2026-07-30

## Why this needed reverse engineering

File bytes do not travel over the 32/64-byte control packet stream. MWB opens a
**second TCP connection to the same port**, identical to the control channel up
to and including the IV exchange, and distinguished only by the first packet it
sends.

Recovered from `microsoft/PowerToys`,
`src/modules/MouseWithoutBorders/App/Core/Clipboard.cs`:

```csharp
private const uint MAX_CLIPBOARD_FILE_SIZE_CAN_BE_SENT = 100 * 1024 * 1024; // 100MB
clipboardTcpClient = ConnectToRemoteClipboardSocket(remoteMachine);
ShakeHand(ref remoteMachine, clipboardTcpClient.Client, out Stream enStream, out Stream deStream, ...);
ReceiveAndProcessClipboardData(remoteMachine, clipboardTcpClient.Client, enStream, deStream, postAct);
...
byte[] header = new byte[1024];
fileName = Common.GetStringU(header).Replace("\0", string.Empty);
string[] headers = fileName.Split(Star);
if (headers.Length < 2 || !long.TryParse(headers[0], out long dataSize))
...
m = new FileStream(tempFile, FileMode.Create);
```

Packet types `ClipboardDragDrop (70)`, `ClipboardDragDropEnd (71)`,
`ExplorerDragDrop (72)` and `ClipboardDragDropOp (75)` carry **no file data**.
They coordinate the Windows drag-and-drop UI only.

## Wire format

```
1. TCP connect to the peer on the control port
2. AES-CBC encrypt stream; send a 16-byte random IV block
3. Send one 64-byte DATA packet, Type = Clipboard (69) or ClipboardPush (79)
4. AES-CBC decrypt stream; read the peer's 16-byte IV block and 64-byte packet
5. Send a 1024-byte UTF-16LE header, NUL padded: "<size>*<name>"
6. Send exactly <size> raw bytes
```

The separator is `*`, which is illegal in Windows filenames but legal on Linux,
so only the **first** occurrence is treated as the separator.

A name beginning `text` or `image` marks an oversized clipboard payload rather
than a file. Those stay in memory; MWB does the same.

## Scope

Matches what MWB itself supports, and no more:

- Single file per transfer, both directions
- 100 MB default cap, matching `MAX_CLIPBOARD_FILE_SIZE_CAN_BE_SENT`

Explicit non-goals, because the Windows side does not do them either:

- Folders and multi-file selections. Microsoft's documented workaround is to zip
  first.
- The drag-and-drop UI flow. It needs Explorer `DragEnter`, a helper window and a
  drop form, so it is Windows-only by construction.

## Integration

The file channel shares the control port, so the inbound path must decide what a
connection is from its **first packet, before sending anything**. A file-sending
peer reads our first packet as its header and aborts on an unexpected type, so
sending the control handshake first would break every transfer.

`newSecureStream` was split out of `setupConn` for this. Outbound control is
untouched. Inbound reads one packet, then routes:

- `Clipboard` / `ClipboardPush` to the file receiver
- anything else to `finishControlSetup`, which replays that packet into the
  existing handshake loop

A nil file handler restores the previous behaviour exactly: such connections are
closed.

## Receiving safety

The sender is a remote machine, so the header is hostile input.

| Control | Reason |
|---|---|
| Backslashes normalised before taking a basename | `filepath` on Linux does not treat `\` as a separator, so `..\..\etc\passwd` would otherwise be created literally as one filename |
| Drive letters stripped | Windows peers send `C:\...` |
| Basename only, `.`/`..`/empty rejected | path traversal |
| Leading dot prefixed with `_` | a hostile name must not land as a hidden file |
| `O_EXCL` on the `.part` file | closes the gap between choosing a name and creating it |
| `Lstat`, not `Stat`, when testing for collisions | a dangling symlink must count as occupied, or the create follows it |
| Destination re-checked after `EvalSymlinks` | a symlinked destination directory cannot redirect the write |
| Mode `0600`, never executable | remote content must not arrive runnable |
| Body read through `io.LimitReader` | a peer that under-declares its size cannot overrun the cap |
| Inline buffer never sized from the declared size | otherwise a peer forces a large allocation while sending nothing |
| Free space checked after the directory exists | `statfs` on a missing path fails and would pass by default |
| `.part` then rename | an interrupted transfer never leaves a file that looks whole |

Concurrency is bounded by the existing `maxPendingHandshakes` budget, which the
file path shares.

## Configuration

```toml
file_transfer  = true                  # default
file_dir       = "~/Downloads/mwb"     # default
max_file_size  = 104857600             # default, matches MWB
```

## Known gaps

- Sending is triggered by a `text/uri-list` clipboard selection, so it depends on
  the file manager publishing that target. Most do.
- Only the first file of a multi-file selection would be eligible, so multi-file
  copies are refused with a log line rather than partially sent.
- Not yet exercised against a live Windows peer.
