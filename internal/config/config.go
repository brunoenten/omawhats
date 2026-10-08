package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Font     string  `toml:"font"`
	FontSize float32 `toml:"font_size"`
	Notify   *bool   `toml:"notifications"`
}

func (c Config) NotificationsEnabled() bool { return c.Notify == nil || *c.Notify }

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func Path() string { return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "omawhats", "config.toml") }

func DataDir() string { return filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "omawhats") }

func CacheDir() string { return filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "omawhats") }

func StateDir() string { return xdg("XDG_STATE_HOME", ".local/state") }

// Load reads the config file, falling back to defaults.
func Load() Config {
	var c Config
	_, _ = toml.DecodeFile(Path(), &c)
	if c.Font == "" {
		c.Font = systemMonospace()
	}
	if c.FontSize <= 0 {
		c.FontSize = 14
	}
	return c
}

// fallbackFonts cover glyphs that terminal fonts usually lack (arrows, emoji).
var fallbackFonts = []string{"Adwaita Mono", "DejaVu Sans Mono", "Noto Sans Symbols 2", "Noto Color Emoji"}

// Typeface returns a Gio typeface list for the configured font. Fonts are
// indexed by their legacy family name (e.g. "JetBrainsMono NF" rather than
// "JetBrainsMono Nerd Font"), so every alias fontconfig knows is included.
func (c Config) Typeface() string {
	families := []string{c.Font}
	if out, err := exec.Command("fc-match", "-f", "%{family}", c.Font).Output(); err == nil {
		for _, f := range strings.Split(string(out), ",") {
			if f = strings.TrimSpace(f); f != "" {
				families = append(families, f)
			}
		}
	}
	return strings.Join(append(families, fallbackFonts...), ", ")
}

func systemMonospace() string {
	out, err := exec.Command("fc-match", "-f", "%{family[0]}", "monospace").Output()
	if err != nil || len(out) == 0 {
		return "monospace"
	}
	return strings.TrimSpace(string(out))
}
