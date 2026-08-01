# File transfer design

Status: implemented
Date: 2026-07-30

Protocol source: Microsoft PowerToys `microsoft/PowerToys` at
`d2c53bf3861ed2688a1c30aafd66ea0fc0186399` (verified 2026-08-01).

## Why this needed reverse engineering

File bytes do not travel over the 32/64-byte control packet stream. MWB opens a
**second TCP connection to the base/clipboard port**. Control uses
`TcpPort + 1`; file and oversized clipboard transfers use `TcpPort`. The two
connections share encryption and identity, but not the packet framing after the
IV exchange.

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

`SocketStuff` creates the two distinct listeners:

```csharp
skMessageServer = new TcpServer(TcpPort + 1, TCPServerThread);
skClipboardServer = new TcpServer(TcpPort, AcceptConnectionAndSendClipboardData);
```

Packet types `ClipboardDragDrop (70)`, `ClipboardDragDropEnd (71)`,
`ExplorerDragDrop (72)` and `ClipboardDragDropOp (75)` carry **no file data**.
They coordinate the Windows drag-and-drop UI only.

## Wire format

```
1. TCP connect to the peer on the base/clipboard port
2. AES-CBC encrypt stream; send a 16-byte random IV block
3. Send one raw, unstamped 64-byte DATA struct, Type = Clipboard (69) or ClipboardPush (79)
4. AES-CBC decrypt stream; read the peer's 16-byte IV block and raw 64-byte DATA struct
5. Send a 1024-byte UTF-16LE header, NUL padded: "<size>*<name>"
6. Send exactly <size> raw bytes
```

The raw header is intentionally different from a control packet. PowerToys
calls `enStream.Write(package.Bytes, ...)` directly inside `ShakeHand`; it does
not pass the struct through the magic/checksum stamping path. Bytes 1-3 must
therefore remain zero. The header's `MachineName` and `Src` must also match the
active control connection, or PowerToys rejects it with
`ResolveID(name) == package.Src` / `IsConnectedTo(package.Src)`.

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

The file channel has a dedicated listener on the base port. The control listener
remains on base+1. Both sides write their raw DATA header before reading the
other's, then the `ClipboardPush` value decides which side sends the payload.

`newSecureStream` is shared so both channels use the same encryption prefix, but
the file path then uses `sendChannelHeader` / `recvChannelHeader` instead of
`SendPacket` / `RecvPacket`.

The clipboard event flow is asymmetric in PowerToys and both halves matter:

- Linux -> Windows: the clipboard poll detects `text/uri-list`, opens a
  `ClipboardPush` channel, and uses `PostAction.Other`. PowerToys stages the
  received file on its clipboard, so the user can paste into any Explorer
  folder.
- Windows -> Linux: PowerToys broadcasts a `Clipboard` beat when a file is
  copied. Like PowerToys, Linux remembers that announcement for 30 seconds and
  answers with `ClipboardAsk` only when Linux becomes active. That transition
  can come from `MachineSwitched`, `NextMachine`, or the bidirectional
  capturer's own virtual-edge return (which has no inbound packet). PowerToys
  then connects back with `ClipboardPush`. Linux
  receives the file into private `$XDG_CACHE_HOME/mwb/clipboard` backing
  storage and publishes `x-special/gnome-copied-files` on GNOME or the generic
  `text/uri-list` elsewhere; the file manager creates a visible destination
  only when the user pastes. A mere Windows copy therefore does not populate or
  spam a Linux download folder.

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
| Declared size capped before receive; aligned reader writes exactly that size | a peer that under-declares its size cannot overrun the cap |
| Inline buffer never sized from the declared size | otherwise a peer forces a large allocation while sending nothing |
| Free space checked after the directory exists | `statfs` on a missing path fails and would pass by default |
| `.part` then rename | an interrupted transfer never leaves a file that looks whole |

Concurrency is bounded by the existing `maxPendingHandshakes` budget, which the
file path shares.

## Configuration

```toml
file_transfer  = true                  # default
max_file_size  = 104857600             # default, matches MWB
```

`file_dir` is a deprecated compatibility setting and is ignored. Clipboard
backing files are intentionally kept out of user-visible directories; only the
newest selection remains in the private cache.

## Known gaps

- Sending is triggered by a `text/uri-list` clipboard selection, so it depends on
  the file manager publishing that target. Most do.
- Only the first file of a multi-file selection would be eligible, so multi-file
  copies are refused with a log line rather than partially sent.
- The protocol transfers copied files; PowerToys' Explorer drag/drop UI is not
  available on Linux.
