package filetransfer

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"time"
)

// notifyTimeout bounds the notification helper. A wedged notification daemon
// must never hold up the transfer path.
const notifyTimeout = 5 * time.Second

// Notify tells the user a file arrived, using notify-send when it is present.
//
// Absence is not an error: the repo already treats xclip and xdotool the same
// way, and a missing notification daemon should not make a working transfer
// look like a failure.
func Notify(res *Result) {
	if res == nil || res.Path == "" {
		return
	}

	bin, err := exec.LookPath("notify-send")
	if err != nil {
		slog.Debug("notify-send unavailable; skipping desktop notification")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()

	body := res.Name + " (" + strconv.FormatInt(res.Size, 10) + " bytes)\n" + res.Path
	cmd := exec.CommandContext(ctx, bin,
		"--app-name=mwb",
		"--icon=document-save",
		"File received from remote machine",
		body)
	if err := cmd.Run(); err != nil {
		slog.Debug("notify-send failed", "err", err)
	}
}
