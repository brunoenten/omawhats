// Package notify sends desktop notifications through notify-send.
package notify

import (
	"os/exec"
	"strings"
)

const maxBody = 240

func Send(title, body string) {
	body = strings.TrimSpace(body)
	if r := []rune(body); len(r) > maxBody {
		body = string(r[:maxBody]) + "…"
	}
	cmd := exec.Command("notify-send", "--app-name=omawhats", "--expire-time=5000", title, body)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
