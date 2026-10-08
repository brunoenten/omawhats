package wa

import (
	"math"
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func (c *Client) onMessage(evt *events.Message) {
	if c.db == nil {
		return
	}
	eff, ok, err := c.apply(c.db, evt, true)
	if err != nil {
		c.Store.SetStatus("saving message: " + err.Error())
		return
	}
	if !ok {
		return
	}
	c.refreshChat(eff.k)
	c.refreshMessage(eff.k, eff.id)
	if !eff.incoming || c.OnIncoming == nil {
		return
	}
	ch, ok := c.Store.Chat(eff.k)
	if !ok {
		return
	}
	if m, ok := c.db.message(eff.k, eff.id); ok {
		c.OnIncoming(ch, c.withNames([]Message{m})[0])
	}
}

func (c *Client) onHistory(evt *events.HistorySync) {
	cli := c.client()
	data := evt.Data
	if cli == nil || c.db == nil || data == nil {
		return
	}
	typ := data.GetSyncType()
	if typ == waHistorySync.HistorySync_PUSH_NAME {
		c.renamed()
		return
	}
	withState := typ == waHistorySync.HistorySync_INITIAL_BOOTSTRAP || typ == waHistorySync.HistorySync_RECENT

	tx, err := c.db.Begin()
	if err != nil {
		c.Store.SetStatus("history: " + err.Error())
		return
	}
	added := map[ChatKey]int{}
	for _, conv := range data.GetConversations() {
		jid, err := types.ParseJID(conv.GetID())
		if err != nil || skip(jid) {
			continue
		}
		if jid.Server == types.HiddenUserServer && conv.GetPnJID() != "" {
			if pn, err := types.ParseJID(conv.GetPnJID()); err == nil {
				jid = pn
			}
		}
		k := c.key(jid)
		if err := ensureChat(tx, k); err != nil {
			break
		}
		added[k] += 0
		if name := conv.GetName(); name != "" {
			_ = setChat(tx, k, "name", name)
		} else if name := conv.GetDisplayName(); name != "" && !k.IsGroup() {
			_ = setChat(tx, k, "name", name)
		}
		if withState {
			unread := int(conv.GetUnreadCount())
			if conv.GetMarkedAsUnread() && unread == 0 {
				unread = 1
			}
			_ = setChat(tx, k, "unread", unread)
			_ = setChat(tx, k, "pinned", conv.GetPinned() > 0)
			_ = setChat(tx, k, "archived", conv.GetArchived())
			_ = setChat(tx, k, "muted_until", muteEnd(conv.GetMuteEndTime()))
			if ts := int64(conv.GetConversationTimestamp()); ts > 0 {
				_, _ = tx.Exec(`UPDATE chats SET last_ts = ? WHERE jid = ? AND last_ts < ?`, ts, k, ts)
			}
		}
		for _, hm := range conv.GetMessages() {
			me, err := cli.ParseWebMessage(jid, hm.GetMessage())
			if err != nil {
				continue
			}
			eff, _, err := c.apply(tx, me, false)
			if err != nil {
				c.Store.SetStatus("history: " + err.Error())
				continue
			}
			if eff.inserted {
				added[eff.k]++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		c.Store.SetStatus("history: " + err.Error())
		return
	}

	c.reloadChats()
	onDemand := typ == waHistorySync.HistorySync_ON_DEMAND
	for k, n := range added {
		c.mergeHistory(k, n, onDemand)
	}
}

func muteEnd(v uint64) int64 {
	switch {
	case v == 0:
		return 0
	case v > math.MaxInt64:
		return math.MaxInt64
	case v > 1e12:
		return int64(v / 1000)
	}
	return int64(v)
}

// mergeHistory pulls newly synced messages into an open chat.
func (c *Client) mergeHistory(k ChatKey, added int, onDemand bool) {
	s := c.Store
	s.mu.Lock()
	loaded, pending := s.loaded[k], c.pending[k]
	var oldest *Message
	if ms := s.messages[k]; len(ms) > 0 {
		m := ms[0]
		oldest = &m
	}
	if onDemand && pending {
		delete(c.pending, k)
		s.loading[k] = false
		if added == 0 {
			s.complete[k] = true
		}
	}
	s.mu.Unlock()
	if !loaded || added == 0 {
		s.changed()
		return
	}
	msgs, err := c.db.page(k, nil, historyPage)
	if err == nil && onDemand && oldest != nil {
		var older []Message
		older, err = c.db.page(k, oldest, historyPage)
		msgs = append(older, msgs...)
	}
	if err != nil {
		s.SetStatus("history: " + err.Error())
		return
	}
	c.withNames(msgs)
	s.update(func() { s.upsertLocked(k, msgs...) })
}

// LoadHistory loads the latest page of a chat, or the page before the oldest
// loaded message if older is set. Once the local history runs out, older
// messages are requested from the phone.
func (c *Client) LoadHistory(k ChatKey, older bool) {
	s := c.Store
	s.mu.Lock()
	if c.db == nil || s.loading[k] || (older && s.complete[k]) {
		s.mu.Unlock()
		return
	}
	var before *Message
	if older && len(s.messages[k]) > 0 {
		m := s.messages[k][0]
		before = &m
	}
	s.loading[k] = true
	s.mu.Unlock()

	go func() {
		var msgs []Message
		var err error
		if !older || before != nil {
			msgs, err = c.db.page(k, before, historyPage)
		}
		c.withNames(msgs)
		s.mu.Lock()
		s.loading[k] = false
		if err == nil {
			s.upsertLocked(k, msgs...)
			s.loaded[k] = true
		}
		s.mu.Unlock()
		if err != nil {
			s.SetStatus("history: " + err.Error())
		} else if older && len(msgs) == 0 {
			c.requestOlder(k, before)
		}
		s.changed()
	}()
}

func (c *Client) requestOlder(k ChatKey, before *Message) {
	s := c.Store
	cli, ok := c.connected()
	if !ok || before == nil {
		s.update(func() { s.complete[k] = true })
		return
	}
	s.update(func() {
		s.loading[k] = true
		c.pending[k] = true
	})
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: k.jid(), IsFromMe: before.Out, IsGroup: k.IsGroup()},
		ID:            before.ID,
		Timestamp:     before.Date,
	}
	if _, err := cli.SendPeerMessage(c.ctx, cli.BuildHistorySyncRequest(info, historyPage)); err != nil {
		s.SetStatus("history request: " + err.Error())
		c.giveUp(k)
		return
	}
	time.AfterFunc(30*time.Second, func() { c.giveUp(k) })
}

// giveUp ends an unanswered history request; the phone may be offline or
// have nothing older.
func (c *Client) giveUp(k ChatKey) {
	s := c.Store
	s.mu.Lock()
	if !c.pending[k] {
		s.mu.Unlock()
		return
	}
	delete(c.pending, k)
	s.loading[k] = false
	s.complete[k] = true
	s.mu.Unlock()
	s.changed()
}
