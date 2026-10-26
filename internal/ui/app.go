// Package ui is the Gio front end.
package ui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"rsc.io/qr"

	"github.com/brunoenten/omawhats/internal/config"
	"github.com/brunoenten/omawhats/internal/notify"
	"github.com/brunoenten/omawhats/internal/wa"
	"github.com/brunoenten/omawhats/internal/theme"
)

type nav int

const (
	navChats nav = iota
	navMessages
)

type UI struct {
	w      *app.Window
	cfg    config.Config
	mth    *material.Theme
	th     *theme.Theme
	store  *wa.Store
	client atomic.Pointer[wa.Client]
	cancel context.CancelFunc

	focused atomic.Bool
	activeK atomic.Value // wa.ChatKey

	root      int
	nav       nav
	active    wa.ChatKey
	selChat   wa.ChatKey
	selMsg    string
	replyTo   string
	editing   string
	drafts    map[wa.ChatKey]string
	wantFocus event.Tag
	title     string

	visible  []wa.Chat
	msgs     []wa.Message
	items    []item
	anchor   string
	nameCols int
	reveal   bool

	// Cache store snapshots so we don't copy chats/messages every frame.
	storeGen   uint64
	searchQ    string
	refreshKey wa.ChatKey

	imgs        *images
	photoClicks map[string]*widget.Clickable
	reactClicks map[string]*widget.Clickable
	rowTags     map[string]*rowTag
	pickClicks  [8]widget.Clickable
	picker      string
	pickerSel   int

	composer widget.Editor
	search   widget.Editor
	chatList layout.List
	msgList  layout.List
	clicks   []widget.Clickable

	charW, lineH int
	measuredAt   unit.Sp
	qrURL        string
	qrCode       *qr.Code
}

func Run(cfg config.Config) error {
	w := new(app.Window)
	w.Option(app.Title("omawhats"), app.Size(1100, 720), app.MinSize(520, 320), app.Decorated(false))

	u := &UI{w: w, cfg: cfg, drafts: map[wa.ChatKey]string{}}
	u.activeK.Store(wa.ChatKey(""))
	u.store = wa.NewStore(w.Invalidate)
	u.imgs = newImages(filepath.Join(config.CacheDir(), "photos"), w.Invalidate)
	u.photoClicks = map[string]*widget.Clickable{}
	u.reactClicks = map[string]*widget.Clickable{}
	u.rowTags = map[string]*rowTag{}
	shaper := text.NewShaper(text.WithCollection(gofont.Collection()))
	u.mth = &material.Theme{Shaper: shaper, TextSize: unit.Sp(cfg.FontSize), Face: font.Typeface(cfg.Typeface())}
	u.composer.Submit = true
	u.search.SingleLine, u.search.Submit = true, true
	u.chatList.Axis = layout.Vertical
	u.msgList.Axis, u.msgList.ScrollToEnd = layout.Vertical, true

	theme.Watch(w.Invalidate)

	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	go u.startClient(ctx)

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			cancel()
			return e.Err
		case app.ConfigEvent:
			u.focused.Store(e.Config.Focused)
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.frame(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// startClient runs the WhatsApp client.
func (u *UI) startClient(ctx context.Context) {
	cfg := u.cfg
	c := wa.New(config.DataDir(), u.store)
	c.OnIncoming = func(ch wa.Chat, m wa.Message) {
		if !cfg.NotificationsEnabled() || ch.Muted() || ch.Archived || time.Since(m.Date) > 2*time.Minute {
			return
		}
		if u.focused.Load() && u.activeK.Load().(wa.ChatKey) == ch.Key {
			return
		}
		body := m.Summary()
		if ch.Group() {
			body = m.Sender + ": " + body
		}
		notify.Send(ch.Title, body)
	}
	u.client.Store(c)
	u.w.Invalidate()
	c.Run(ctx)
}

func (u *UI) frame(gtx layout.Context) {
	u.th = theme.Current()
	u.mth.Palette = material.Palette{Bg: u.th.Bg, Fg: u.th.Fg, ContrastBg: u.th.Accent, ContrastFg: u.th.Bg}
	paint.Fill(gtx.Ops, u.th.Bg)
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &u.root)
	area.Pop()
	u.measure(gtx)

	c := u.client.Load()
	auth := u.store.Auth()
	ready := c != nil && auth.State == wa.AuthReady

	if ready {
		q := u.search.Text()
		gen := u.store.Gen()
		if gen != u.storeGen || q != u.searchQ || u.active != u.refreshKey {
			u.storeGen, u.searchQ, u.refreshKey = gen, q, u.active
			u.refresh()
		}
	}
	u.handleKeys(gtx, ready)

	switch {
	case !ready:
		u.layoutLogin(gtx, auth)
	default:
		u.layoutMain(gtx)
		if u.active != "" && u.focused.Load() {
			if loaded, _, _ := u.store.HistoryState(u.active); loaded {
				c.MarkRead(u.active)
			}
		}
	}

	u.syncFocus(gtx)
	u.syncTitle()
}

// refresh pulls the current chat list and messages from the store.
func (u *UI) refresh() {
	q := strings.ToLower(strings.TrimSpace(u.search.Text()))
	all := u.store.Chats()
	u.visible = u.visible[:0]
	for _, ch := range all {
		if q == "" || strings.Contains(strings.ToLower(ch.Title), q) {
			u.visible = append(u.visible, ch)
		}
	}
	if u.chatIndex(u.selChat) < 0 && len(u.visible) > 0 {
		u.selChat = u.visible[0].Key
	}
	u.msgs = nil
	if u.active != "" {
		u.msgs = u.store.Messages(u.active)
	}
	if u.selMsg != "" && u.msgIndex(u.selMsg) < 0 {
		u.selMsg = ""
	}
}

func (u *UI) chatIndex(k wa.ChatKey) int {
	for i, c := range u.visible {
		if c.Key == k {
			return i
		}
	}
	return -1
}

func (u *UI) msgIndex(id string) int {
	for i, m := range u.msgs {
		if m.ID == id {
			return i
		}
	}
	return -1
}

func (u *UI) syncFocus(gtx layout.Context) {
	if u.wantFocus != nil {
		gtx.Execute(key.FocusCmd{Tag: u.wantFocus})
		u.wantFocus = nil
		return
	}
	if !gtx.Focused(&u.composer) && !gtx.Focused(&u.search) && !gtx.Focused(&u.root) {
		gtx.Execute(key.FocusCmd{Tag: &u.root})
	}
}

func (u *UI) syncTitle() {
	t := "omawhats"
	if n := u.store.TotalUnread(); n > 0 {
		t = fmt.Sprintf("omawhats (%d)", n)
	}
	if t != u.title {
		u.title = t
		u.w.Option(app.Title(t))
	}
}

func (u *UI) open(k wa.ChatKey) {
	c := u.client.Load()
	if c == nil {
		return
	}
	if u.active != "" {
		u.drafts[u.active] = u.composer.Text()
	}
	u.active, u.selChat = k, k
	u.activeK.Store(k)
	u.selMsg, u.replyTo, u.editing = "", "", ""
	clear(u.photoClicks)
	clear(u.reactClicks)
	clear(u.rowTags)
	u.picker = ""
	u.composer.SetText(u.drafts[k])
	u.composer.SetCaret(u.composer.Len(), u.composer.Len())
	u.msgList.Position = layout.Position{}
	u.anchor = ""
	u.nav = navMessages
	u.wantFocus = &u.composer
	if loaded, _, _ := u.store.HistoryState(k); !loaded {
		c.LoadHistory(k, false)
	}
}

func (u *UI) moveChat(delta int) {
	if len(u.visible) == 0 {
		return
	}
	i := u.chatIndex(u.selChat) + delta
	i = max(0, min(i, len(u.visible)-1))
	u.selChat = u.visible[i].Key
	u.revealChat(i)
}

func (u *UI) revealChat(i int) {
	p := &u.chatList.Position
	if i < p.First {
		p.First, p.Offset = i, 0
	} else if p.Count > 1 && i >= p.First+p.Count-1 {
		p.First, p.Offset = i-p.Count+2, 0
	}
}

// moveMsg moves the message selection; moving past the newest message
// returns to following the bottom of the chat.
func (u *UI) moveMsg(delta int) {
	if len(u.msgs) == 0 {
		return
	}
	i := u.msgIndex(u.selMsg)
	if i < 0 {
		if delta > 0 {
			return
		}
		i = len(u.msgs)
	}
	i += delta
	switch {
	case i >= len(u.msgs):
		u.selMsg = ""
		u.msgList.Position.BeforeEnd = false
		return
	case i < 0:
		i = 0
		if c := u.client.Load(); c != nil {
			c.LoadHistory(u.active, true)
		}
	}
	u.selMsg = u.msgs[i].ID
	u.reveal = true
}

func (u *UI) selected() (wa.Message, bool) {
	if i := u.msgIndex(u.selMsg); i >= 0 {
		return u.msgs[i], true
	}
	return wa.Message{}, false
}

func (u *UI) submit(txt string) {
	c := u.client.Load()
	txt = strings.TrimSpace(txt)
	if c == nil || u.active == "" || txt == "" {
		return
	}
	if u.editing != "" {
		c.Edit(u.active, u.editing, txt)
	} else {
		c.Send(u.active, txt, u.replyTo)
	}
	u.composer.SetText("")
	u.drafts[u.active] = ""
	u.replyTo, u.editing, u.selMsg = "", "", ""
	u.msgList.Position.BeforeEnd = false
}

func (u *UI) handleKeys(gtx layout.Context, ready bool) {
	root := &u.root
	rk := func(name key.Name) key.Filter { return key.Filter{Focus: root, Name: name, Optional: key.ModShift} }
	filters := []event.Filter{
		key.FocusFilter{Target: root},
		key.Filter{Name: key.NameEscape},
		key.Filter{Name: "Q", Required: key.ModCtrl},
		key.Filter{Name: "K", Required: key.ModCtrl},
		key.Filter{Name: "=", Required: key.ModCtrl, Optional: key.ModShift},
		key.Filter{Name: "+", Required: key.ModCtrl, Optional: key.ModShift},
		key.Filter{Name: "-", Required: key.ModCtrl},
		key.Filter{Name: "0", Required: key.ModCtrl},
	}
	if ready {
		for _, n := range []key.Name{"J", "K", "G", "H", "L", "I", "R", "E", "Y", "O", "/", "+", "=",
			"1", "2", "3", "4", "5", "6", "7", "8",
			key.NameReturn, key.NameEnter, key.NameUpArrow, key.NameDownArrow, key.NameLeftArrow,
			key.NameRightArrow, key.NameTab, key.NamePageUp, key.NamePageDown} {
			filters = append(filters, rk(n))
		}
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.onKey(gtx, e, ready)
		}
	}
}

func (u *UI) onKey(gtx layout.Context, e key.Event, ready bool) {
	if e.Modifiers.Contain(key.ModCtrl) {
		switch e.Name {
		case "Q":
			u.w.Perform(system.ActionClose)
		case "K":
			if ready {
				u.wantFocus = &u.search
			}
		case "=", "+":
			u.mth.TextSize = min(u.mth.TextSize+1, 32)
		case "-":
			u.mth.TextSize = max(u.mth.TextSize-1, 8)
		case "0":
			u.mth.TextSize = unit.Sp(u.cfg.FontSize)
		}
		return
	}

	if e.Name == key.NameEscape {
		switch {
		case u.picker != "":
			u.picker = ""
		case gtx.Focused(&u.composer):
			if u.editing != "" {
				u.editing = ""
				u.composer.SetText(u.drafts[u.active])
			} else if u.replyTo != "" {
				u.replyTo = ""
			} else {
				u.wantFocus = &u.root
			}
		case gtx.Focused(&u.search):
			u.search.SetText("")
			u.wantFocus = &u.root
		case u.nav == navMessages && u.selMsg != "":
			u.selMsg = ""
			u.msgList.Position.BeforeEnd = false
		case u.nav == navMessages:
			u.nav = navChats
		}
		return
	}

	shift := e.Modifiers.Contain(key.ModShift)
	if u.picker != "" {
		switch e.Name {
		case "1", "2", "3", "4", "5", "6", "7", "8":
			u.pick(int(e.Name[0] - '1'))
		case "H", key.NameLeftArrow:
			u.pickerSel = (u.pickerSel + len(palette) - 1) % len(palette)
		case "L", key.NameRightArrow, key.NameTab:
			u.pickerSel = (u.pickerSel + 1) % len(palette)
		case key.NameReturn, key.NameEnter:
			u.pick(u.pickerSel)
		}
		return
	}
	if u.nav == navChats {
		switch e.Name {
		case "J", key.NameDownArrow:
			u.moveChat(1)
		case "K", key.NameUpArrow:
			u.moveChat(-1)
		case key.NamePageDown:
			u.moveChat(10)
		case key.NamePageUp:
			u.moveChat(-10)
		case "G":
			if shift {
				u.moveChat(len(u.visible))
			} else {
				u.moveChat(-len(u.visible))
			}
		case "L", key.NameRightArrow, key.NameReturn, key.NameEnter:
			if u.chatIndex(u.selChat) >= 0 {
				u.open(u.selChat)
			}
		case key.NameTab, "I":
			if u.active != "" {
				u.nav = navMessages
				if e.Name == "I" {
					u.wantFocus = &u.composer
				}
			}
		case "/":
			u.wantFocus = &u.search
		}
		return
	}

	switch e.Name {
	case "J", key.NameDownArrow:
		u.moveMsg(1)
	case "K", key.NameUpArrow:
		u.moveMsg(-1)
	case key.NamePageDown:
		u.moveMsg(10)
	case key.NamePageUp:
		u.moveMsg(-10)
	case "G":
		if shift {
			u.selMsg = ""
			u.msgList.Position.BeforeEnd = false
		} else {
			u.moveMsg(-len(u.msgs))
		}
	case "H", key.NameLeftArrow, key.NameTab:
		u.nav = navChats
	case "I", key.NameReturn, key.NameEnter:
		u.wantFocus = &u.composer
	case "R":
		if m, ok := u.selected(); ok {
			u.replyTo, u.editing = m.ID, ""
			u.wantFocus = &u.composer
		}
	case "E":
		if m, ok := u.selected(); ok && m.Out && !m.Service {
			u.drafts[u.active] = u.composer.Text()
			u.editing, u.replyTo = m.ID, ""
			u.composer.SetText(m.Text)
			u.composer.SetCaret(u.composer.Len(), u.composer.Len())
			u.wantFocus = &u.composer
		}
	case "+", "=":
		if m, ok := u.selected(); ok && !m.Service {
			u.openPicker(m.ID)
		}
	case "O":
		if m, ok := u.selected(); ok && m.Photo != nil {
			u.openPhoto(m.Photo)
		}
	case "Y":
		if m, ok := u.selected(); ok {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(m.Text))})
			u.store.SetStatus("copied")
		}
	case "/":
		u.wantFocus = &u.search
	}
}

func (u *UI) openPhoto(p *wa.Photo) {
	if c := u.client.Load(); c != nil {
		u.imgs.open(c, p, u.store.SetStatus)
	}
}
