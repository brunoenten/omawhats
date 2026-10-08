package ui

import (
	"fmt"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/brunoenten/omawhats/internal/wa"
)

var palette = []string{"👍", "❤", "🔥", "😂", "😮", "😢", "🙏", "👎"}

// displayEmoji forces emoji presentation; a heart without a variation
// selector would otherwise render as a monochrome text glyph.
func displayEmoji(e string) string {
	if e == "❤" {
		return "❤️"
	}
	return e
}

type rowTag struct{ id string }

// reactable wraps a message row so a right-click anywhere on it opens the
// reaction picker. The row is recorded first so its area encloses the
// children and clicks still reach photos and reaction pills.
func (u *UI) reactable(gtx layout.Context, m wa.Message, row layout.Widget) layout.Dimensions {
	tag := u.rowTags[m.ID]
	if tag == nil {
		tag = &rowTag{m.ID}
		u.rowTags[m.ID] = tag
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Buttons.Contain(pointer.ButtonSecondary) && !m.Service {
			u.openPicker(m.ID)
		}
	}
	rec := op.Record(gtx.Ops)
	dims := row(gtx)
	call := rec.Stop()
	area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	call.Add(gtx.Ops)
	area.Pop()
	return dims
}

func (u *UI) openPicker(id string) {
	u.picker, u.pickerSel = id, 0
	u.selMsg = id
	u.nav = navMessages
	u.wantFocus = &u.root
}

func (u *UI) pick(i int) {
	if i < 0 || i >= len(palette) || u.picker == "" {
		return
	}
	if c := u.client.Load(); c != nil {
		c.React(u.active, u.picker, palette[i])
	}
	u.picker = ""
}

func (u *UI) toggleReaction(id, emoji string) {
	if c := u.client.Load(); c != nil {
		c.React(u.active, id, emoji)
	}
}

// reactions renders existing reactions as clickable "👍 3" pills; ours are
// highlighted with the accent color.
func (u *UI) reactions(gtx layout.Context, m wa.Message) layout.Dimensions {
	if len(m.Reactions) == 0 {
		return layout.Dimensions{}
	}
	children := make([]layout.FlexChild, 0, len(m.Reactions))
	for _, r := range m.Reactions {
		key := fmt.Sprint(m.ID, r.Emoji)
		click := u.reactClicks[key]
		if click == nil {
			click = new(widget.Clickable)
			u.reactClicks[key] = click
		}
		if click.Clicked(gtx) {
			u.toggleReaction(m.ID, r.Emoji)
		}
		c := u.th.FgDim
		if r.Mine {
			c = u.th.Accent
		}
		emoji, count := displayEmoji(r.Emoji), fmt.Sprintf(" %d", r.Count)
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.emoji(gtx, emoji) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, count, c) }),
					)
				})
			})
		}))
	}
	return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx, children...)
	})
}

// picker renders the inline reaction picker for the message it is open on.
func (u *UI) pickerLine(gtx layout.Context) layout.Dimensions {
	th := u.th
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.line(gtx, "react ", th.FgFaint) }),
	}
	for i, e := range palette {
		if u.pickClicks[i].Clicked(gtx) {
			u.pick(i)
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.pickClicks[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				w := u.pad(0.5, 0, func(gtx layout.Context) layout.Dimensions {
					return u.emoji(gtx, displayEmoji(e))
				})
				if i == u.pickerSel {
					return withBg(gtx, th.Accent, w)
				}
				return w(gtx)
			})
		}))
	}
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return u.line(gtx, "  1-8 · esc", th.FgFaint)
	}))
	return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
}
