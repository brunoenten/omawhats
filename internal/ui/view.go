package ui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/brunoenten/omawhats/internal/wa"
)

const maxNameCols = 12

type item struct {
	day  string
	msg  int
	key  string
	cont bool
}

func (u *UI) layoutMain(gtx layout.Context) {
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			paneW := min(34*u.charW, gtx.Constraints.Max.X*2/5)
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = paneW, paneW
					return u.layoutChats(gtx)
				}),
				layout.Rigid(vrule(u.th.Rule)),
				layout.Flexed(1, u.layoutMessages),
			)
		}),
		layout.Rigid(u.layoutStatus),
	)
}

func (u *UI) header(gtx layout.Context, left, right layout.Widget) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.pad(1, 0.5, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, left),
				layout.Rigid(right),
			)
		})),
		layout.Rigid(hrule(u.th.Rule)),
	)
}

func (u *UI) layoutChats(gtx layout.Context) layout.Dimensions {
	for {
		ev, ok := u.search.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent:
			u.refresh()
			if len(u.visible) > 0 {
				u.selChat = u.visible[0].Key
				u.chatList.Position = layout.Position{}
			}
		case widget.SubmitEvent:
			if u.chatIndex(u.selChat) >= 0 {
				k := u.selChat
				u.search.SetText("")
				u.refresh()
				u.open(k)
			}
		}
	}

	searching := gtx.Focused(&u.search) || u.search.Len() > 0
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.header(gtx,
				func(gtx layout.Context) layout.Dimensions {
					if !searching {
						return u.line(gtx, "chats", u.th.FgDim)
					}
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "/ ", u.th.Accent) }),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.editor(gtx, &u.search, "search")
						}),
					)
				},
				func(gtx layout.Context) layout.Dimensions {
					if n := u.store.TotalUnread(); n > 0 && !searching {
						return u.line(gtx, fmt.Sprint(n), u.th.Accent)
					}
					return layout.Dimensions{}
				})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if len(u.visible) == 0 {
				msg := "loading chats…"
				if searching {
					msg = "no matches"
				}
				return u.pad(1, 0.5, func(gtx layout.Context) layout.Dimensions { return u.line(gtx, msg, u.th.FgFaint) })(gtx)
			}
			if len(u.clicks) < len(u.visible) {
				u.clicks = append(u.clicks, make([]widget.Clickable, len(u.visible)-len(u.clicks))...)
			}
			return u.chatList.Layout(gtx, len(u.visible), func(gtx layout.Context, i int) layout.Dimensions {
				ch := u.visible[i]
				if u.clicks[i].Clicked(gtx) {
					u.open(ch.Key)
				}
				return u.clicks[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.chatRow(gtx, ch)
				})
			})
		}),
	)
}

func (u *UI) chatRow(gtx layout.Context, ch wa.Chat) layout.Dimensions {
	th := u.th
	sel := ch.Key == u.selChat
	marker, markerC := "  ", th.Accent
	if ch.Key == u.active {
		marker = "▌ "
	}
	titleC, prevC, countC := th.Fg, th.FgFaint, th.Accent
	if ch.Unread > 0 {
		titleC = th.FgBright
	}
	if ch.Muted() {
		titleC, countC = th.FgDim, th.FgFaint
	}
	if ch.Key == u.active {
		titleC = th.Accent
	}

	row := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return u.pad(0, 0.3, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(u.cols(2, func(gtx layout.Context) layout.Dimensions { return u.line(gtx, marker, markerC) })),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.line(gtx, ch.Title, titleC) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if ch.Unread == 0 {
								return u.cols(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} })(gtx)
							}
							return u.line(gtx, fmt.Sprintf(" %d ", ch.Unread), countC)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(u.cols(2, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} })),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.line(gtx, oneLine(ch.Preview), prevC)
						}),
						layout.Rigid(u.cols(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} })),
					)
				}),
			)
		})(gtx)
	}
	switch {
	case sel && u.nav == navChats && !gtx.Focused(&u.search):
		return withBg(gtx, th.Selection, row)
	case sel:
		return withBg(gtx, th.BgLight, row)
	}
	return row(gtx)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (u *UI) buildItems() {
	u.items = u.items[:0]
	u.nameCols = 1
	var prev *wa.Message
	for i := range u.msgs {
		m := &u.msgs[i]
		if prev == nil || !sameDay(prev.Date, m.Date) {
			u.items = append(u.items, item{day: dayLabel(m.Date), key: "day:" + m.ID})
			prev = nil
		}
		cont := prev != nil && !prev.Service && !m.Service && prev.SenderID == m.SenderID &&
			m.Date.Sub(prev.Date) < 5*time.Minute
		u.items = append(u.items, item{msg: i, key: m.ID, cont: cont})
		if !m.Service {
			u.nameCols = max(u.nameCols, min(maxNameCols, len([]rune(m.Sender))))
		}
		prev = m
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func dayLabel(t time.Time) string {
	now := time.Now()
	switch {
	case sameDay(t, now):
		return "today"
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "yesterday"
	case t.Year() == now.Year():
		return strings.ToLower(t.Format("Monday, 02 Jan"))
	}
	return strings.ToLower(t.Format("Monday, 02 Jan 2006"))
}

func (u *UI) layoutMessages(gtx layout.Context) layout.Dimensions {
	th := u.th
	if u.active == "" {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "omawhats", th.Accent) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, " ", th.Fg) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.line(gtx, "j/k move · enter open · / search · ctrl+q quit", th.FgFaint)
				}),
			)
		})
	}

	chat, _ := u.store.Chat(u.active)
	loaded, loading, complete := u.store.HistoryState(u.active)
	u.buildItems()

	for {
		ev, ok := u.composer.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok {
			u.submit(s.Text)
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.header(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, chat.Title, th.FgBright) }),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.line(gtx, "  "+chatKind(chat), th.FgFaint)
						}),
					)
				},
				func(gtx layout.Context) layout.Dimensions {
					if loading {
						return u.line(gtx, "loading…", th.FgFaint)
					}
					return layout.Dimensions{}
				})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if loaded && len(u.items) == 0 {
				return u.pad(1, 0.5, func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "no messages", th.FgFaint) })(gtx)
			}
			return u.layoutMessageList(gtx, loaded && !complete && !loading)
		}),
		layout.Rigid(hrule(th.Rule)),
		layout.Rigid(u.layoutComposer),
	)
}

func chatKind(c wa.Chat) string {
	var parts []string
	if c.Group() {
		parts = append(parts, "group")
	}
	if c.Muted() {
		parts = append(parts, "muted")
	}
	return strings.Join(parts, " · ")
}

func (u *UI) layoutMessageList(gtx layout.Context, canLoadOlder bool) layout.Dimensions {
	p := &u.msgList.Position
	// Keep the viewport on the same message when older history is prepended.
	if p.BeforeEnd && u.anchor != "" {
		for i, it := range u.items {
			if it.key == u.anchor && i != p.First {
				p.First = i
				break
			}
		}
	}
	if u.reveal {
		u.reveal = false
		for i, it := range u.items {
			if it.key != u.selMsg {
				continue
			}
			if i <= p.First || (p.Count > 1 && i >= p.First+p.Count-1) {
				u.msgList.ScrollTo(max(0, i-2))
			}
			if i == len(u.items)-1 {
				p.BeforeEnd = false
			}
			break
		}
	}

	dims := u.msgList.Layout(gtx, len(u.items), func(gtx layout.Context, i int) layout.Dimensions {
		return u.messageRow(gtx, u.items[i])
	})

	if p.First < len(u.items) {
		u.anchor = u.items[p.First].key
	}
	if canLoadOlder && p.First == 0 && p.BeforeEnd {
		if c := u.client.Load(); c != nil {
			c.LoadHistory(u.active, true)
		}
	}
	return dims
}

func (u *UI) messageRow(gtx layout.Context, it item) layout.Dimensions {
	th := u.th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	if it.day != "" {
		return u.pad(1, 0.4, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "── "+it.day+" ", th.FgFaint) }),
				layout.Flexed(1, hrule(th.Rule)),
			)
		})(gtx)
	}

	m := u.msgs[it.msg]
	nameC := th.NameColor(m.SenderID)
	if m.Out {
		nameC = th.Accent
	}
	name := m.Sender
	if it.cont {
		name = ""
	}
	// One-on-one chats drop the name column; our side is told apart by
	// dimmer text and an accent separator. A chat with yourself has no other side.
	private := !m.Peer.IsGroup()
	textC, sepC := th.Fg, th.Rule
	if private && m.Out && string(m.Peer) != m.SenderID {
		textC, sepC = th.FgDim, th.Accent
	}
	stamp := m.Date.Format("15:04")
	if m.Edited {
		stamp += "~"
	}

	body := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if m.ReplyTo == "" {
					return layout.Dimensions{}
				}
				ref := "↳ message"
				if r, ok := u.store.Message(m.Peer, m.ReplyTo); ok {
					ref = "↳ " + r.Sender + ": " + oneLine(r.Summary())
				}
				return u.line(gtx, ref, th.FgFaint)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if m.Photo == nil {
					return layout.Dimensions{}
				}
				return u.photo(gtx, m)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if m.File == nil {
					return layout.Dimensions{}
				}
				return u.fileRow(gtx, m)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				switch {
				case m.Service:
					return u.label(gtx, m.Sender+" "+m.Text, th.FgDim)
				case m.Text == "":
					return layout.Dimensions{}
				}
				return u.linkLabel(gtx, m.ID, m.Text, textC)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.reactions(gtx, m) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if u.picker != m.ID {
					return layout.Dimensions{}
				}
				return u.pickerLine(gtx)
			}),
		)
	}

	nameW := u.nameCols
	if private {
		nameW = 0
	}
	row := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		dims := u.pad(1, 0.1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(u.cols(7, func(gtx layout.Context) layout.Dimensions {
					return u.line(gtx, stamp, th.FgFaint)
				})),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if private {
						return layout.Dimensions{}
					}
					return u.cols(nameW, func(gtx layout.Context) layout.Dimensions {
						if m.Service {
							return u.lineAligned(gtx, "*", th.FgFaint, text.End)
						}
						return u.lineAligned(gtx, name, nameC, text.End)
					})(gtx)
				}),
				layout.Rigid(u.cols(3, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(0, u.lineH)}
				})),
				layout.Flexed(1, body),
			)
		})(gtx)
		// The separator spans the whole row, padding included, so consecutive
		// messages form one continuous rule.
		x := (1+7+nameW)*u.charW + u.charW*3/2
		rule := image.Rect(x, 0, x+max(1, gtx.Dp(1)), dims.Size.Y)
		paint.FillShape(gtx.Ops, sepC, clip.Rect(rule).Op())
		return dims
	}
	return u.reactable(gtx, m, func(gtx layout.Context) layout.Dimensions {
		if m.ID == u.selMsg {
			bg := th.BgLight
			if u.nav == navMessages {
				bg = th.Selection
			}
			return withBg(gtx, bg, row)
		}
		return row(gtx)
	})
}

func (u *UI) editor(gtx layout.Context, ed *widget.Editor, hint string) layout.Dimensions {
	e := material.Editor(u.mth, ed, hint)
	e.Color = u.th.Fg
	e.HintColor = u.th.FgFaint
	e.SelectionColor = u.th.Selection
	return e.Layout(gtx)
}

func (u *UI) layoutComposer(gtx layout.Context) layout.Dimensions {
	th := u.th
	focused := gtx.Focused(&u.composer)
	return u.pad(1, 0.5, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var ctx string
				c := th.FgDim
				switch {
				case u.editing != "":
					ctx, c = "~ editing message", th.Yellow
				case u.replyTo != "":
					ctx = "↳ reply"
					if r, ok := u.store.Message(u.active, u.replyTo); ok {
						ctx = "↳ replying to " + r.Sender + ": " + oneLine(r.Summary())
					}
				default:
					return layout.Dimensions{}
				}
				return layout.Inset{Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.line(gtx, ctx, c) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "  esc cancel", th.FgFaint) }),
					)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				promptC := th.FgFaint
				if focused {
					promptC = th.Accent
				}
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(u.cols(2, func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "›", promptC) })),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, 8*u.lineH)
						return u.editor(gtx, &u.composer, "write a message")
					}),
				)
			}),
		)
	})(gtx)
}

func (u *UI) mode(gtx layout.Context) string {
	switch {
	case u.picker != "":
		return "REACT"
	case gtx.Focused(&u.composer):
		return "INSERT"
	case gtx.Focused(&u.search):
		return "SEARCH"
	case u.nav == navMessages:
		return "MESSAGES"
	}
	return "CHATS"
}

var hints = map[string]string{
	"CHATS":    "j/k move  enter open  / search  tab messages  ^q quit",
	"MESSAGES": "j/k select  r reply  e edit  + react  o open  a attach  y copy  i write  h chats",
	"REACT":    "1-8 pick  h/l move  enter pick  esc cancel",
	"INSERT":   "enter send  shift+enter newline  esc normal",
	"SEARCH":   "type to filter  enter open  esc cancel",
}

func (u *UI) layoutStatus(gtx layout.Context) layout.Dimensions {
	th := u.th
	mode := u.mode(gtx)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return withBg(gtx, th.BgDark, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return withBg(gtx, th.Accent, u.pad(1, 0.2, func(gtx layout.Context) layout.Dimensions {
					return u.line(gtx, mode, th.Bg)
				}))
			}),
			layout.Rigid(u.pad(1, 0.2, func(gtx layout.Context) layout.Dimensions {
				if u.store.Online() {
					return u.line(gtx, "● online", th.Green)
				}
				return u.line(gtx, "○ connecting", th.Yellow)
			})),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if s := u.store.Status(); s != "" {
					gtx.Execute(op.InvalidateCmd{At: time.Now().Add(time.Second)})
					return u.line(gtx, s, th.FgDim)
				}
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}),
			layout.Rigid(u.pad(1, 0.2, func(gtx layout.Context) layout.Dimensions {
				return u.line(gtx, hints[mode], th.FgFaint)
			})),
		)
	})
}

func (u *UI) centered(gtx layout.Context, rows ...layout.Widget) layout.Dimensions {
	children := make([]layout.FlexChild, len(rows))
	for i, r := range rows {
		children[i] = layout.Rigid(r)
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

func (u *UI) layoutLogin(gtx layout.Context, a wa.Auth) {
	th := u.th
	row := func(s string, kind int) layout.Widget {
		c := th.Fg
		switch kind {
		case 1:
			c = th.FgDim
		case 2:
			c = th.FgFaint
		case 3:
			c = th.Accent
		case 4:
			c = th.Red
		}
		return func(gtx layout.Context) layout.Dimensions { return u.line(gtx, s, c) }
	}
	blank := row(" ", 0)

	switch a.State {
	case wa.AuthConnecting:
		u.centered(gtx, row("omawhats", 3), blank, row("connecting to whatsapp…", 1))
	case wa.AuthFailed:
		u.centered(gtx, row("omawhats", 3), blank, row(a.Error, 4), row("retrying…", 2))
		gtx.Execute(op.InvalidateCmd{At: time.Now().Add(time.Second)})
	case wa.AuthQR:
		left := max(0, int(time.Until(a.QRExpires).Seconds()))
		u.centered(gtx,
			row("omawhats", 3), blank,
			func(gtx layout.Context) layout.Dimensions { return u.drawQR(gtx, a.QRURL, th.Fg, th.Bg) },
			blank,
			row("scan with whatsapp on your phone", 0),
			row("settings › linked devices › link a device", 1),
			blank,
			row(fmt.Sprintf("code refreshes in %ds", left), 2),
		)
		gtx.Execute(op.InvalidateCmd{At: time.Now().Add(time.Second)})
	case wa.AuthCode:
		u.centered(gtx,
			row("omawhats", 3), blank,
			row("check that your phone shows this code", 0),
			blank,
			row(a.Code, 3),
		)
	}
}

// photo draws an inline photo, scaled to fit the text column. Until the full
// image arrives it shows the embedded thumbnail, or a same-sized placeholder.
func (u *UI) fileRow(gtx layout.Context, m wa.Message) layout.Dimensions {
	f := m.File
	click := u.fileClicks[m.ID]
	if click == nil {
		click = new(widget.Clickable)
		u.fileClicks[m.ID] = click
	}
	if click.Clicked(gtx) {
		u.openFile(f)
	}
	return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return u.line(gtx, f.Label, u.th.Cyan)
	})
}

func (u *UI) photo(gtx layout.Context, m wa.Message) layout.Dimensions {
	th, p := u.th, m.Photo
	maxW := float32(min(gtx.Constraints.Max.X, 52*u.charW))
	maxH := float32(14 * u.lineH)
	w, h := float32(max(p.W, 1)), float32(max(p.H, 1))
	scale := min(maxW/w, maxH/h, gtx.Metric.PxPerDp)
	size := image.Pt(max(int(w*scale), u.charW), max(int(h*scale), u.lineH))

	click := u.photoClicks[m.ID]
	if click == nil {
		click = new(widget.Clickable)
		u.photoClicks[m.ID] = click
	}
	if click.Clicked(gtx) {
		u.openPhoto(p)
	}

	e := imgEntry{state: imgFailed}
	if c := u.client.Load(); c != nil {
		e = u.imgs.get(c, p)
	}
	return layout.Inset{Top: 2, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(size)
		return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if e.hasOp {
				return widget.Image{Src: e.op, Fit: widget.Fill}.Layout(gtx)
			}
			msg := "loading photo…"
			if e.state == imgFailed {
				msg = "[photo unavailable]"
			}
			return withBg(gtx, th.BgLight, func(gtx layout.Context) layout.Dimensions {
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return u.line(gtx, msg, th.FgFaint)
				})
			})
		})
	})
}
