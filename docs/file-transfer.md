# File transfer

MWB Linux follows PowerToys Mouse Without Borders behavior: one copied file can
be transferred in either direction, with a default limit of 100 MB. Folders and
multi-file selections are not supported; zip them first.

## Channel flow

File bytes do not travel inside normal MWB control packets. PowerToys opens a
second encrypted TCP connection on the base clipboard port, normally 15100.
The control connection uses the adjacent port, normally 15101.

After the encrypted streams are ready, both peers exchange a raw 64-byte
clipboard channel header. The side sending data then writes:

1. A 1024-byte UTF-16LE header padded with NUL bytes: `<size>*<name>`.
2. Exactly `<size>` file bytes, padded to the protocol block boundary on the
   encrypted stream.

The raw channel header is not stamped like a control packet. Its machine name
and source ID must match the active control connection or PowerToys rejects the
channel.

## Clipboard behavior

### Linux to Windows

When the Linux clipboard contains one file URI, MWB Linux validates the file,
opens a push channel, and sends it to PowerToys. PowerToys puts the received file
on the Windows clipboard so the user can paste it into a chosen folder.

### Windows to Linux

PowerToys first broadcasts a clipboard notification. When control moves to
Linux, MWB Linux asks for the pending clipboard item and PowerToys connects back
with the file.

The received file is kept in private cache storage and published as a Linux
file clipboard selection. Copying alone does not create a visible file; the
file manager creates the destination when the user pastes. Only the newest
staged selection is retained.

## Receive-side safety

The remote header and filename are treated as untrusted input:

- Windows separators and drive prefixes are normalized before taking a
  basename.
- Empty names, traversal names, separators, NUL bytes, and hidden leading dots
  are rejected or rewritten safely.
- The resolved destination is checked to remain inside the staging directory.
- Existing paths and dangling symlinks count as collisions.
- Data is written with mode `0600` to an exclusive `.part` file and renamed
  only after the declared body arrives completely.
- Declared sizes are capped before receive, copying is bounded, and free space
  is checked before writing.
- Inline clipboard buffers grow from received data rather than allocating from
  a peer-controlled size.
- Known zero-byte PowerToys refusal headers are returned as errors instead of
  becoming fake files. Ordinary empty files remain valid.

Concurrent inbound handshakes share the network package's bounded connection
budget.

## Configuration

```toml
file_transfer = true
max_file_size = 104857600
```

`file_dir` is retained only for configuration compatibility and is ignored.
Received clipboard files use private cache storage.

## PowerToys references

- [Mouse Without Borders documentation](https://learn.microsoft.com/windows/powertoys/mouse-without-borders)
- [PowerToys clipboard implementation](https://github.com/microsoft/PowerToys/blob/main/src/modules/MouseWithoutBorders/App/Core/Clipboard.cs)
- [PowerToys socket implementation](https://github.com/microsoft/PowerToys/blob/main/src/modules/MouseWithoutBorders/App/Class/SocketStuff.cs)
