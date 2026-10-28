package ui

import (
	"os/exec"
	"strings"
)

// pickFile opens a system file chooser and returns the selected path.
// An empty path with a nil error means the user cancelled.
func pickFile() (string, error) {
	cmd := exec.Command("zenity", "--file-selection", "--title=Attach file")
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "", nil // cancel
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (u *UI) attach() {
	if u.active == "" || u.editing != "" {
		return
	}
	if !u.picking.CompareAndSwap(false, true) {
		return
	}
	caption := strings.TrimSpace(u.composer.Text())
	replyTo := u.replyTo
	k := u.active
	go func() {
		defer u.picking.Store(false)
		path, err := pickFile()
		if err != nil {
			u.store.SetStatus("attach: " + err.Error())
			return
		}
		if path == "" {
			return
		}
		c := u.client.Load()
		if c == nil {
			u.store.SetStatus("attach: not connected")
			return
		}
		c.SendFile(k, path, caption, replyTo)
		u.pendingClear.Store(true)
		u.w.Invalidate()
	}()
}
