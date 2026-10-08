package wa

import (
	"context"
	"errors"
	"os"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var errOffline = errors.New("not connected")

// Send sends a text message, optionally as a reply.
func (c *Client) Send(k ChatKey, text, replyTo string) {
	cli, ok := c.connected()
	if !ok {
		c.Store.SetStatus("send: " + errOffline.Error())
		return
	}
	msg := &waE2E.Message{Conversation: proto.String(text)}
	if replyTo != "" {
		ext := &waE2E.ExtendedTextMessage{Text: proto.String(text), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(replyTo)}}
		if r, ok := c.Store.Message(k, replyTo); ok {
			ext.ContextInfo.Participant = proto.String(r.senderJID)
			ext.ContextInfo.QuotedMessage = &waE2E.Message{Conversation: proto.String(r.Summary())}
		}
		msg = &waE2E.Message{ExtendedTextMessage: ext}
	}
	self := c.self()
	go func() {
		resp, err := cli.SendMessage(context.WithoutCancel(c.ctx), k.jid(), msg)
		if err != nil {
			c.Store.SetStatus("send: " + err.Error())
			return
		}
		m := Message{
			ID: resp.ID, Peer: k, Out: true, Date: resp.Timestamp, Text: text, ReplyTo: replyTo,
			SenderID: self.String(), senderJID: self.String(),
		}
		if err := ensureChat(c.db, k); err == nil {
			if inserted, err := insertMessage(c.db, m); err == nil && inserted {
				_ = bumpChat(c.db, k, m.Date.Unix(), preview(m))
			}
		}
		c.refreshChat(k)
		c.refreshMessage(k, m.ID)
	}()
}

// Edit replaces the text of one of our messages.
func (c *Client) Edit(k ChatKey, id, text string) {
	cli, ok := c.connected()
	m, found := c.Store.Message(k, id)
	switch {
	case !ok:
		c.Store.SetStatus("edit: " + errOffline.Error())
		return
	case !found || !m.Out:
		return
	case m.Photo != nil:
		c.Store.SetStatus("edit: photo captions can't be edited")
		return
	case time.Since(m.Date) > whatsmeow.EditWindow:
		c.Store.SetStatus("edit: too old to edit")
		return
	}
	if err := editMessage(c.db, k, id, text); err != nil {
		c.Store.SetStatus("edit: " + err.Error())
		return
	}
	c.refreshMessage(k, id)
	c.refreshPreview(k)
	go func() {
		chat := k.jid()
		edit := cli.BuildEdit(chat, id, &waE2E.Message{Conversation: proto.String(text)})
		if _, err := cli.SendMessage(context.WithoutCancel(c.ctx), chat, edit); err != nil {
			c.Store.SetStatus("edit: " + err.Error())
		}
	}()
}

// refreshPreview recomputes a chat's preview from its newest message.
func (c *Client) refreshPreview(k ChatKey) {
	ms, err := c.db.page(k, nil, 1)
	if err != nil || len(ms) == 0 {
		return
	}
	if _, err := c.db.Exec(`UPDATE chats SET preview = ? WHERE jid = ?`, preview(ms[0]), k); err == nil {
		c.refreshChat(k)
	}
}

// React toggles our reaction on a message. WhatsApp allows one reaction per
// person: picking the one we already set removes it, anything else replaces it.
func (c *Client) React(k ChatKey, id, emoji string) {
	cli, ok := c.connected()
	m, found := c.Store.Message(k, id)
	if !ok {
		c.Store.SetStatus("reaction: " + errOffline.Error())
		return
	}
	if !found {
		return
	}
	for _, r := range m.Reactions {
		if r.Mine && r.Emoji == emoji {
			emoji = ""
		}
	}
	if err := setReaction(c.db, k, id, "me", emoji, true); err != nil {
		c.Store.SetStatus("reaction: " + err.Error())
		return
	}
	c.refreshMessage(k, id)
	sender, err := types.ParseJID(m.senderJID)
	if err != nil {
		return
	}
	go func() {
		chat := k.jid()
		if _, err := cli.SendMessage(context.WithoutCancel(c.ctx), chat, cli.BuildReaction(chat, sender, id, emoji)); err != nil {
			c.Store.SetStatus("reaction: " + err.Error())
		}
	}()
}

// MarkRead sends read receipts for the unread messages of a chat.
func (c *Client) MarkRead(k ChatKey) {
	cli, ok := c.connected()
	s := c.Store
	s.mu.Lock()
	ch, found := s.chats[k]
	if !ok || !found || ch.Unread == 0 {
		s.mu.Unlock()
		return
	}
	n := ch.Unread
	ch.Unread = 0
	bySender := map[string][]types.MessageID{}
	ms := s.messages[k]
	for i := len(ms) - 1; i >= 0 && n > 0; i-- {
		if m := ms[i]; !m.Out && !m.Service {
			bySender[m.senderJID] = append(bySender[m.senderJID], m.ID)
			n--
		}
	}
	s.mu.Unlock()
	s.changed()

	go func() {
		_ = setChat(c.db, k, "unread", 0)
		chat := k.jid()
		for from, ids := range bySender {
			sender := types.EmptyJID
			if k.IsGroup() {
				sender, _ = types.ParseJID(from)
			}
			if err := cli.MarkRead(context.WithoutCancel(c.ctx), ids, time.Now(), chat, sender); err != nil {
				s.SetStatus("mark read: " + err.Error())
			}
		}
	}()
}

// DownloadPhoto saves the full-size image of a photo to path.
func (c *Client) DownloadPhoto(p Photo, path string) error {
	cli, ok := c.connected()
	if !ok {
		return errOffline
	}
	var img waE2E.ImageMessage
	if err := proto.Unmarshal(p.media, &img); err != nil {
		return err
	}
	data, err := cli.Download(c.ctx, &img)
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
