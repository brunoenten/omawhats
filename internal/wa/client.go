package wa

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

const historyPage = 50

var errLoggedOut = errors.New("logged out")

type Client struct {
	Store *Store

	// OnIncoming is called for every new incoming live message.
	OnIncoming func(Chat, Message)

	dataDir   string
	ctx       context.Context
	db        *db
	container *sqlstore.Container
	loggedOut chan struct{}

	mu  sync.RWMutex
	cli *whatsmeow.Client

	namesMu sync.Mutex
	names   map[string]string

	// pending holds chats with an on-demand history request in flight.
	pending   map[ChatKey]bool
	groupInfo map[ChatKey]bool
}

func New(dataDir string, s *Store) *Client {
	store.DeviceProps.Os = proto.String("omawhats")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	return &Client{
		Store:     s,
		dataDir:   dataDir,
		loggedOut: make(chan struct{}, 1),
		names:     map[string]string{},
		pending:   map[ChatKey]bool{},
		groupInfo: map[ChatKey]bool{},
	}
}

func (c *Client) client() *whatsmeow.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cli
}

func (c *Client) connected() (*whatsmeow.Client, bool) {
	cli := c.client()
	return cli, cli != nil && cli.IsLoggedIn()
}

// Run connects, pairs if needed and processes events until ctx is cancelled.
// It retries on failure.
func (c *Client) Run(ctx context.Context) {
	c.ctx = ctx
	for ctx.Err() == nil {
		err := c.run(ctx)
		if ctx.Err() != nil {
			return
		}
		c.Store.setOnline(false)
		if err != nil {
			c.Store.SetStatus("disconnected: " + err.Error())
			if a := c.Store.Auth(); a.State != AuthReady || errors.Is(err, errLoggedOut) {
				c.Store.setAuth(Auth{State: AuthFailed, Error: err.Error()})
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *Client) run(ctx context.Context) error {
	if err := os.MkdirAll(c.dataDir, 0o700); err != nil {
		return err
	}
	if c.db == nil {
		d, err := openDB(filepath.Join(c.dataDir, "messages.db"))
		if err != nil {
			return err
		}
		c.db = d
	}
	if c.container == nil {
		dsn := "file:" + filepath.Join(c.dataDir, "device.db") + "?_foreign_keys=on&_busy_timeout=5000"
		container, err := sqlstore.New(ctx, "sqlite3", dsn, waLog.Noop)
		if err != nil {
			return err
		}
		c.container = container
	}
	device, err := c.container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}

	cli := whatsmeow.NewClient(device, waLog.Noop)
	cli.AddEventHandler(c.handle)
	c.mu.Lock()
	c.cli = cli
	c.mu.Unlock()
	defer cli.Disconnect()

	if cli.Store.ID == nil {
		if err := c.pair(ctx, cli); err != nil {
			return err
		}
	} else {
		c.Store.setAuth(Auth{State: AuthReady})
		c.reloadChats()
		if err := cli.Connect(); err != nil {
			return err
		}
	}

	select {
	case <-ctx.Done():
		return nil
	case <-c.loggedOut:
		c.forget()
		return errLoggedOut
	}
}

func (c *Client) pair(ctx context.Context, cli *whatsmeow.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := cli.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := cli.Connect(); err != nil {
		return err
	}
	for item := range ch {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			c.Store.setAuth(Auth{State: AuthQR, QRURL: item.Code, QRExpires: time.Now().Add(item.Timeout)})
		case whatsmeow.QRChannelEventPasskeyResponse:
			c.Store.setAuth(Auth{State: AuthCode, Code: item.PasskeyConfirmation.Code})
			if err := cli.SendPasskeyConfirmation(ctx); err != nil {
				return err
			}
		case whatsmeow.QRChannelEventPasskeyRequest:
			return errors.New("this account requires passkey pairing, which omawhats does not support")
		case whatsmeow.QRChannelSuccess.Event:
			c.Store.setAuth(Auth{State: AuthReady})
			c.reloadChats()
			return nil
		case whatsmeow.QRChannelEventError:
			return item.Error
		case whatsmeow.QRChannelTimeout.Event:
			return errors.New("qr code expired")
		default:
			return fmt.Errorf("pairing failed: %s", item.Event)
		}
	}
	return errors.New("pairing cancelled")
}

// forget drops all local history after the device was unlinked.
func (c *Client) forget() {
	if err := c.db.wipe(); err != nil {
		c.Store.SetStatus("clearing history: " + err.Error())
	}
	c.namesMu.Lock()
	clear(c.names)
	c.namesMu.Unlock()
	c.Store.update(func() {
		clear(c.Store.chats)
		clear(c.Store.messages)
		clear(c.Store.loading)
		clear(c.Store.loaded)
		clear(c.Store.complete)
	})
}

func (c *Client) handle(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		c.Store.setOnline(true)
		go c.syncGroups()
	case *events.Disconnected:
		c.Store.setOnline(false)
	case *events.StreamReplaced:
		c.Store.setOnline(false)
		c.Store.SetStatus("this session was opened elsewhere")
	case *events.TemporaryBan:
		c.Store.SetStatus("temporarily banned: " + e.String())
	case *events.LoggedOut:
		select {
		case c.loggedOut <- struct{}{}:
		default:
		}
	case *events.Message:
		c.onMessage(e)
	case *events.HistorySync:
		c.onHistory(e)
	case *events.Receipt:
		if e.IsFromMe && (e.Type == types.ReceiptTypeRead || e.Type == types.ReceiptTypeReadSelf) {
			c.setChat(c.key(e.Chat), "unread", 0)
		}
	case *events.Pin:
		c.setChat(c.key(e.JID), "pinned", e.Action.GetPinned())
	case *events.Archive:
		c.setChat(c.key(e.JID), "archived", e.Action.GetArchived())
	case *events.Mute:
		var until int64
		if e.Action.GetMuted() {
			until = math.MaxInt64
			if end := e.Action.GetMuteEndTimestamp(); end > 0 {
				until = end / 1000
			}
		}
		c.setChat(c.key(e.JID), "muted_until", until)
	case *events.MarkChatAsRead:
		k := c.key(e.JID)
		if e.Action.GetRead() {
			c.setChat(k, "unread", 0)
		} else if ch, ok := c.Store.Chat(k); ok && ch.Unread == 0 {
			c.setChat(k, "unread", 1)
		}
	case *events.PushName:
		c.renamed(e.JID, e.JIDAlt)
	case *events.Contact:
		c.renamed(e.JID)
	case *events.GroupInfo:
		if e.Name != nil {
			c.setChat(c.key(e.JID), "name", e.Name.Name)
		}
	case *events.JoinedGroup:
		c.setChat(c.key(e.JID), "name", e.GroupInfo.Name)
	}
}

func (c *Client) setChat(k ChatKey, col string, v any) {
	if k == "" || c.db == nil {
		return
	}
	if err := setChat(c.db, k, col, v); err != nil {
		c.Store.SetStatus("saving chat: " + err.Error())
		return
	}
	c.refreshChat(k)
}

// skip reports chats omawhats does not show: status updates, broadcast
// lists and channels.
func skip(j types.JID) bool {
	return j.IsEmpty() || j.Server == types.BroadcastServer || j.Server == types.NewsletterServer
}

// norm strips the device and maps a LID to its phone-number JID when known.
func (c *Client) norm(j types.JID) types.JID {
	j = j.ToNonAD()
	if j.Server != types.HiddenUserServer {
		return j
	}
	if cli := c.client(); cli != nil {
		if pn, err := cli.Store.LIDs.GetPNForLID(c.ctx, j); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return j
}

func (c *Client) key(j types.JID) ChatKey {
	if skip(j) {
		return ""
	}
	return ChatKey(c.norm(j).String())
}

func (c *Client) self() types.JID {
	if cli := c.client(); cli != nil && cli.Store.ID != nil {
		return cli.Store.ID.ToNonAD()
	}
	return types.EmptyJID
}

// name returns a display name for a user JID string.
func (c *Client) name(id string) string {
	c.namesMu.Lock()
	n, ok := c.names[id]
	c.namesMu.Unlock()
	if ok {
		return n
	}
	j, err := types.ParseJID(id)
	if err != nil {
		return id
	}
	n = c.contactName(j)
	if n == "" {
		n = phone(j)
	}
	c.namesMu.Lock()
	c.names[id] = n
	c.namesMu.Unlock()
	return n
}

func (c *Client) contactName(j types.JID) string {
	cli := c.client()
	if cli == nil {
		return ""
	}
	ci, err := cli.Store.Contacts.GetContact(c.ctx, j)
	if err != nil || !ci.Found {
		return ""
	}
	for _, n := range []string{ci.FullName, ci.FirstName, ci.PushName, ci.BusinessName} {
		if n != "" {
			return n
		}
	}
	return ""
}

func phone(j types.JID) string {
	if j.Server == types.DefaultUserServer {
		return "+" + j.User
	}
	return j.User
}

// renamed drops cached names after a contact or push name changed.
func (c *Client) renamed(jids ...types.JID) {
	c.namesMu.Lock()
	for _, j := range jids {
		if !j.IsEmpty() {
			delete(c.names, c.norm(j).String())
		}
	}
	c.namesMu.Unlock()
	c.reloadChats()
	s := c.Store
	s.update(func() {
		for k, ms := range s.messages {
			for i := range ms {
				if !ms[i].Out {
					ms[i].Sender = c.name(ms[i].SenderID)
				}
			}
			s.messages[k] = ms
		}
	})
}

func (c *Client) title(r chatRow) string {
	switch {
	case r.key.IsGroup():
		if r.name != "" {
			return r.name
		}
		return "group"
	case r.key.jid() == c.self():
		return "you"
	}
	if n := c.contactName(r.key.jid()); n != "" {
		return n
	}
	if r.name != "" {
		return r.name
	}
	return phone(r.key.jid())
}

func (c *Client) toChat(r chatRow) *Chat {
	if r.key.IsGroup() && r.name == "" {
		c.fetchGroupName(r.key)
	}
	return &Chat{
		Key: r.key, Title: c.title(r), Unread: r.unread, Preview: r.preview, LastDate: int(r.last),
		Pinned: r.pinned, MutedUntil: r.muted, Archived: r.archived,
	}
}

func (c *Client) reloadChats() {
	if c.db == nil {
		return
	}
	rows, err := c.db.chats()
	if err != nil {
		c.Store.SetStatus("loading chats: " + err.Error())
		return
	}
	chats := make(map[ChatKey]*Chat, len(rows))
	for _, r := range rows {
		chats[r.key] = c.toChat(r)
	}
	c.Store.update(func() { c.Store.chats = chats })
}

func (c *Client) refreshChat(k ChatKey) {
	r, ok := c.db.chat(k)
	if !ok {
		return
	}
	ch := c.toChat(r)
	c.Store.update(func() { c.Store.chats[k] = ch })
}

func (c *Client) fetchGroupName(k ChatKey) {
	cli, ok := c.connected()
	c.Store.mu.Lock()
	if !ok || c.groupInfo[k] {
		c.Store.mu.Unlock()
		return
	}
	c.groupInfo[k] = true
	c.Store.mu.Unlock()
	go func() {
		info, err := cli.GetGroupInfo(c.ctx, k.jid())
		if err == nil && info.Name != "" {
			c.setChat(k, "name", info.Name)
		}
	}()
}

// syncGroups refreshes the names of the groups we already have chats for.
func (c *Client) syncGroups() {
	cli, ok := c.connected()
	if !ok {
		return
	}
	groups, err := cli.GetJoinedGroups(c.ctx)
	if err != nil {
		return
	}
	for _, g := range groups {
		if g.Name != "" {
			_, _ = c.db.Exec(`UPDATE chats SET name = ? WHERE jid = ?`, g.Name, c.key(g.JID))
		}
	}
	c.reloadChats()
}

// withNames fills in display names for messages loaded from the database.
func (c *Client) withNames(ms []Message) []Message {
	for i := range ms {
		if ms[i].Out {
			ms[i].Sender = "you"
		} else {
			ms[i].Sender = c.name(ms[i].SenderID)
		}
	}
	return ms
}

// refreshMessage reloads one message into the store if its chat is open.
func (c *Client) refreshMessage(k ChatKey, id string) {
	s := c.Store
	s.mu.RLock()
	loaded := s.loaded[k]
	s.mu.RUnlock()
	if !loaded {
		return
	}
	if m, ok := c.db.message(k, id); ok {
		m = c.withNames([]Message{m})[0]
		s.update(func() { s.upsertLocked(k, m) })
	}
}

func preview(m Message) string {
	if m.Out {
		return "you: " + m.Summary()
	}
	return m.Summary()
}
