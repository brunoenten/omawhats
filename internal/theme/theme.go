// Package theme maps the active Omarchy theme (colors.toml) onto UI colors.
package theme

import (
	"hash/fnv"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/brunoenten/omawhats/internal/config"
)

type Theme struct {
	Bg, BgDark, BgLight color.NRGBA
	Fg, FgDim, FgBright color.NRGBA
	FgFaint             color.NRGBA
	Accent, Selection   color.NRGBA
	Rule                color.NRGBA
	Red, Yellow, Green  color.NRGBA
	Cyan, Blue, Magenta color.NRGBA
	Orange              color.NRGBA
	Names               []color.NRGBA
}

var current atomic.Pointer[Theme]

func Current() *Theme {
	if t := current.Load(); t != nil {
		return t
	}
	t := load()
	current.Store(t)
	return t
}

func candidates() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(config.StateDir(), "omarchy", "current", "theme", "colors.toml"),
		filepath.Join(home, ".config", "omarchy", "current", "theme", "colors.toml"),
	}
}

func source() (string, time.Time) {
	for _, p := range candidates() {
		if st, err := os.Stat(p); err == nil {
			return p, st.ModTime()
		}
	}
	return "", time.Time{}
}

// Watch reloads the theme whenever the Omarchy theme changes and calls onChange.
func Watch(onChange func()) {
	go func() {
		path, mod := source()
		for range time.Tick(1500 * time.Millisecond) {
			p, m := source()
			if p == path && m.Equal(mod) {
				continue
			}
			path, mod = p, m
			current.Store(load())
			onChange()
		}
	}()
}

func load() *Theme {
	raw := map[string]any{}
	if p, _ := source(); p != "" {
		_, _ = toml.DecodeFile(p, &raw)
	}
	get := func(def string, keys ...string) color.NRGBA {
		for _, k := range keys {
			if s, ok := raw[k].(string); ok {
				if c, ok := parseHex(s); ok {
					return c
				}
			}
		}
		c, _ := parseHex(def)
		return c
	}

	t := &Theme{
		Bg:        get("#000000", "background"),
		BgDark:    get("#0d0d0d", "dark_background", "background"),
		BgLight:   get("#121212", "lighter_background", "color0"),
		Fg:        get("#efefef", "foreground"),
		FgDim:     get("#8a8a8d", "light_foreground", "color8"),
		FgFaint:   get("#555555", "dark_foreground", "color8"),
		FgBright:  get("#ffffff", "bright_foreground", "color15", "foreground"),
		Accent:    get("#407e70", "accent", "color4"),
		Selection: get("#333333", "selection", "selection_background"),
		Rule:      get("#1e1e1e", "muted", "color0"),
		Red:       get("#d35f5f", "red", "color1"),
		Green:     get("#8a9a7b", "green", "color2"),
		Yellow:    get("#ffc107", "yellow", "color3"),
		Blue:      get("#8a9fbe", "blue", "color4"),
		Magenta:   get("#c1a1c1", "magenta", "color5"),
		Cyan:      get("#88aabb", "cyan", "color6"),
		Orange:    get("#f59e0b", "orange", "bright_yellow", "color11"),
	}
	t.Names = []color.NRGBA{t.Cyan, t.Magenta, t.Green, t.Blue, t.Yellow, t.Orange, t.Red}
	return t
}

func parseHex(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 && len(s) != 8 {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s[:6], 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}

// NameColor picks a stable color for a sender.
func (t *Theme) NameColor(id string) color.NRGBA {
	h := fnv.New32a()
	h.Write([]byte(id))
	return t.Names[h.Sum32()%uint32(len(t.Names))]
}
