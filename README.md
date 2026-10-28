# omawhats

A minimalist, keyboard-driven WhatsApp client for [Omarchy](https://omarchy.org).

Native Wayland app written in Go with [Gio](https://gioui.org) and
[whatsmeow](https://github.com/tulir/whatsmeow): no webview, no bubbles, no
avatars. Monospace text, a vim-style status bar, and colors taken live from
your active Omarchy theme. It is the WhatsApp sibling of omagram.

```
── today ─────────────────────────────────────────────
10:07      you │ building a whatsapp client that fits omarchy
10:09      dhh │ ↳ you: building a whatsapp client that…
               │ love it. bubbles are a crime against text
10:19      dhh │ shipped 3.2 🎉
```

## Features

- QR-code login: omawhats links as a companion device, like WhatsApp Web
- Chat list with unread counts, pinned, muted and archived chats (archived ones are hidden), incremental search
- History from WhatsApp's initial sync, stored locally; scrolling past it asks your phone for older messages
- Send, reply, and edit messages (within WhatsApp's edit window)
- Reactions: see them under each message, right-click (or `+`) to react, click yours to remove it
- Desktop notifications via `notify-send` (mako / Omarchy shell)
- Follows `omarchy theme set` without restarting
- Uses your system monospace font, with fallbacks for symbols and color emoji

Photos are shown inline: the embedded thumbnail first, then the full image (`o` or a click opens it in your image viewer). Documents, audio, video and stickers show as cyan labels (`[file report.pdf]`, `[voice 0:12]`); `o` or a click downloads them and opens with `xdg-open`. Press `a` to attach a file (zenity picker): images send as photos, everything else as a document; composer text becomes the caption.

whatsmeow is an unofficial client library. Linking it to your account is the
same mechanism WhatsApp Web uses, but WhatsApp may restrict accounts that use
unofficial clients.

## Install

Requires Go 1.27+, a C compiler (for SQLite) and the usual Wayland development
libraries (present on Omarchy / Arch by default).

```sh
git clone https://github.com/brunoenten/omawhats
cd omawhats
make install   # installs to ~/.local/bin and adds a desktop entry
```

`make run` builds and starts it without installing.

## Login

Start omawhats, then on your phone open WhatsApp › Settings › Linked devices ›
Link a device, and scan the QR code. Your phone then sends recent history,
which can take a minute on the first run.

## Configuration

Everything is optional. `~/.config/omawhats/config.toml`:

```toml
font          = "JetBrainsMono Nerd Font"  # default: fontconfig's monospace
font_size     = 14
notifications = true
```

The linked device lives in `~/.local/share/omawhats/device.db` and the message
history in `~/.local/share/omawhats/messages.db`. Unlinking the device from your
phone clears the history and shows a new QR code. Downloaded photos and files are
cached in `~/.cache/omawhats/photos` and `~/.cache/omawhats/files`.

## Keys

| Context  | Keys |
|----------|------|
| Chats    | `j`/`k` move · `g`/`G` top/bottom · `enter` open · `/` search · `tab` messages |
| Messages | `j`/`k` select · `r` reply · `e` edit · `+` react · `o` open · `a` attach · `y` copy · `i` write · `h` back |
| Reacting | `1`–`8` pick · `h`/`l` move · `enter` pick · `esc` cancel |
| Writing  | `enter` send · `shift+enter` newline · `esc` cancel / back |
| Global   | `ctrl+k` search · `ctrl+=`/`ctrl+-`/`ctrl+0` font size · `ctrl+q` quit |

## Hyprland

The window class is `omawhats`, e.g. for a keybinding or window rule.

## License

[MIT](LICENSE)
