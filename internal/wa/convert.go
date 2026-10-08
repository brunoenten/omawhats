package wa

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// effect describes what applying an event changed.
type effect struct {
	k        ChatKey
	id       string
	inserted bool
	incoming bool
}

// apply writes a message event (live or from history sync) to the database.
func (c *Client) apply(ex execer, evt *events.Message, live bool) (effect, bool, error) {
	info := evt.Info
	k := c.key(info.Chat)
	if k == "" {
		return effect{}, false, nil
	}
	if err := ensureChat(ex, k); err != nil {
		return effect{}, false, err
	}
	msg := evt.Message
	sender := c.norm(info.Sender)
	out := info.IsFromMe

	if r := msg.GetReactionMessage(); r != nil {
		target := r.GetKey().GetID()
		reactor := sender.String()
		if out {
			reactor = "me"
		}
		return effect{k: k, id: target}, true, setReaction(ex, k, target, reactor, r.GetText(), out)
	}
	if p := msg.GetProtocolMessage(); p != nil {
		target := p.GetKey().GetID()
		switch p.GetType() {
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			return effect{k: k, id: target}, true, editMessage(ex, k, target, textOf(p.GetEditedMessage()))
		case waE2E.ProtocolMessage_REVOKE:
			return effect{k: k, id: target}, true, revokeMessage(ex, k, target)
		case waE2E.ProtocolMessage_EPHEMERAL_SETTING:
			msg = nil
		default:
			return effect{}, false, nil
		}
	}

	m := Message{
		ID: info.ID, Peer: k, SenderID: sender.String(), senderJID: info.Sender.ToNonAD().String(),
		Out: out, Date: info.Timestamp, Edited: evt.IsEdit,
	}
	switch {
	case evt.Message.GetProtocolMessage().GetType() == waE2E.ProtocolMessage_EPHEMERAL_SETTING:
		m.Service, m.Text = true, "changed disappearing messages"
	case evt.SourceWebMsg.GetMessageStubType() != 0:
		m.Service, m.Text = true, c.stubText(evt.SourceWebMsg)
	default:
		m.Text = textOf(msg)
		if img := msg.GetImageMessage(); img != nil {
			m.Photo = photoOf(m.ID, img)
		} else if label := mediaLabel(msg); label != "" {
			m.Text = strings.TrimSpace(label + " " + m.Text)
		}
		m.ReplyTo = contextOf(msg).GetStanzaID()
	}
	if m.Text == "" && m.Photo == nil {
		return effect{}, false, nil
	}

	inserted, err := insertMessage(ex, m)
	if err != nil || !inserted {
		return effect{k: k, id: m.ID}, err == nil, err
	}
	if err := bumpChat(ex, k, m.Date.Unix(), preview(m)); err != nil {
		return effect{}, false, err
	}
	eff := effect{k: k, id: m.ID, inserted: true}
	if live && !out && !m.Service {
		eff.incoming = true
		if err := addUnread(ex, k); err != nil {
			return effect{}, false, err
		}
	}
	for _, r := range evt.SourceWebMsg.GetReactions() {
		key := r.GetKey()
		mine := key.GetFromMe()
		reactor := "me"
		if !mine {
			from := key.GetParticipant()
			if from == "" {
				from = key.GetRemoteJID()
			}
			j, err := types.ParseJID(from)
			if err != nil {
				continue
			}
			reactor = c.norm(j).String()
		}
		if err := setReaction(ex, k, m.ID, reactor, r.GetText(), mine); err != nil {
			return effect{}, false, err
		}
	}
	return eff, true, nil
}

func textOf(m *waE2E.Message) string {
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetCaption()
	}
	return ""
}

func contextOf(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetLocationMessage() != nil:
		return m.GetLocationMessage().GetContextInfo()
	case m.GetContactMessage() != nil:
		return m.GetContactMessage().GetContextInfo()
	}
	return nil
}

func duration(sec uint32) string {
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// mediaLabel renders non-photo media as a short bracketed label.
func mediaLabel(m *waE2E.Message) string {
	switch {
	case m.GetVideoMessage() != nil:
		if m.GetVideoMessage().GetGifPlayback() {
			return "[gif]"
		}
		return "[video " + duration(m.GetVideoMessage().GetSeconds()) + "]"
	case m.GetPtvMessage() != nil:
		return "[video note " + duration(m.GetPtvMessage().GetSeconds()) + "]"
	case m.GetAudioMessage() != nil:
		a := m.GetAudioMessage()
		if a.GetPTT() {
			return "[voice " + duration(a.GetSeconds()) + "]"
		}
		return "[audio " + duration(a.GetSeconds()) + "]"
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		name := d.GetFileName()
		if name == "" {
			name = d.GetTitle()
		}
		return "[file " + name + "]"
	case m.GetStickerMessage() != nil:
		return "[sticker]"
	case m.GetLocationMessage() != nil:
		l := m.GetLocationMessage()
		if n := l.GetName(); n != "" {
			return "[location " + n + "]"
		}
		return fmt.Sprintf("[location %.5f, %.5f]", l.GetDegreesLatitude(), l.GetDegreesLongitude())
	case m.GetLiveLocationMessage() != nil:
		return "[live location]"
	case m.GetContactMessage() != nil:
		return "[contact " + m.GetContactMessage().GetDisplayName() + "]"
	case m.GetContactsArrayMessage() != nil:
		return "[contacts " + m.GetContactsArrayMessage().GetDisplayName() + "]"
	case m.GetPollCreationMessage() != nil:
		return "[poll " + m.GetPollCreationMessage().GetName() + "]"
	case m.GetPollCreationMessageV2() != nil:
		return "[poll " + m.GetPollCreationMessageV2().GetName() + "]"
	case m.GetPollCreationMessageV3() != nil:
		return "[poll " + m.GetPollCreationMessageV3().GetName() + "]"
	case m.GetEventMessage() != nil:
		return "[event " + m.GetEventMessage().GetName() + "]"
	}
	return ""
}

func photoOf(id string, img *waE2E.ImageMessage) *Photo {
	media, err := proto.Marshal(img)
	if err != nil {
		return nil
	}
	return &Photo{ID: id, W: int(img.GetWidth()), H: int(img.GetHeight()), Thumb: img.GetJPEGThumbnail(), media: media}
}

// stubText describes a history system message, or returns "" to drop it.
func (c *Client) stubText(w *waWeb.WebMessageInfo) string {
	params := w.GetMessageStubParameters()
	names := func() string {
		out := make([]string, 0, len(params))
		for _, p := range params {
			if j, err := types.ParseJID(p); err == nil && j.User != "" {
				out = append(out, c.name(c.norm(j).String()))
			}
		}
		return strings.Join(out, ", ")
	}
	switch w.GetMessageStubType() {
	case waWeb.WebMessageInfo_REVOKE:
		return "deleted this message"
	case waWeb.WebMessageInfo_GROUP_CREATE:
		return "created the group"
	case waWeb.WebMessageInfo_GROUP_CHANGE_SUBJECT:
		if len(params) > 0 {
			return "changed the subject to " + params[0]
		}
		return "changed the subject"
	case waWeb.WebMessageInfo_GROUP_CHANGE_ICON:
		return "changed the group icon"
	case waWeb.WebMessageInfo_GROUP_CHANGE_DESCRIPTION:
		return "changed the group description"
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_ADD:
		return "added " + names()
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_REMOVE:
		return "removed " + names()
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_LEAVE:
		return "left"
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_INVITE, waWeb.WebMessageInfo_GROUP_PARTICIPANT_ACCEPT,
		waWeb.WebMessageInfo_GROUP_PARTICIPANT_LINKED_GROUP_JOIN:
		return "joined"
	case waWeb.WebMessageInfo_CALL_MISSED_VOICE:
		return "missed voice call"
	case waWeb.WebMessageInfo_CALL_MISSED_VIDEO:
		return "missed video call"
	}
	return ""
}
