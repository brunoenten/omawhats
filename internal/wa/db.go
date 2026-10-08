package wa

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// whatsmeow keeps no message history, so everything we have seen lives here.
const schema = `
CREATE TABLE IF NOT EXISTS chats (
	jid         TEXT PRIMARY KEY,
	name        TEXT NOT NULL DEFAULT '',
	unread      INTEGER NOT NULL DEFAULT 0,
	pinned      INTEGER NOT NULL DEFAULT 0,
	muted_until INTEGER NOT NULL DEFAULT 0,
	archived    INTEGER NOT NULL DEFAULT 0,
	last_ts     INTEGER NOT NULL DEFAULT 0,
	preview     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS messages (
	chat       TEXT NOT NULL,
	id         TEXT NOT NULL,
	sender     TEXT NOT NULL,
	sender_jid TEXT NOT NULL,
	from_me    INTEGER NOT NULL,
	ts         INTEGER NOT NULL,
	text       TEXT NOT NULL,
	reply_to   TEXT NOT NULL DEFAULT '',
	edited     INTEGER NOT NULL DEFAULT 0,
	service    INTEGER NOT NULL DEFAULT 0,
	photo_w    INTEGER NOT NULL DEFAULT 0,
	photo_h    INTEGER NOT NULL DEFAULT 0,
	thumb      BLOB,
	media      BLOB,
	PRIMARY KEY (chat, id)
);
CREATE INDEX IF NOT EXISTS messages_ts ON messages (chat, ts, id);
CREATE TABLE IF NOT EXISTS reactions (
	chat    TEXT NOT NULL,
	msg     TEXT NOT NULL,
	reactor TEXT NOT NULL,
	emoji   TEXT NOT NULL,
	mine    INTEGER NOT NULL,
	PRIMARY KEY (chat, msg, reactor)
);
`

type db struct{ *sql.DB }

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func openDB(path string) (*db, error) {
	d, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(schema); err != nil {
		d.Close()
		return nil, err
	}
	return &db{d}, nil
}

func (d *db) wipe() error {
	_, err := d.Exec(`DELETE FROM reactions; DELETE FROM messages; DELETE FROM chats;`)
	return err
}

type chatRow struct {
	key      ChatKey
	name     string
	unread   int
	pinned   bool
	muted    int64
	archived bool
	last     int64
	preview  string
}

const chatCols = `jid, name, unread, pinned, muted_until, archived, last_ts, preview`

func scanChat(sc interface{ Scan(...any) error }) (chatRow, error) {
	var r chatRow
	err := sc.Scan(&r.key, &r.name, &r.unread, &r.pinned, &r.muted, &r.archived, &r.last, &r.preview)
	return r, err
}

func (d *db) chats() ([]chatRow, error) {
	rows, err := d.Query(`SELECT ` + chatCols + ` FROM chats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chatRow
	for rows.Next() {
		r, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *db) chat(k ChatKey) (chatRow, bool) {
	r, err := scanChat(d.QueryRow(`SELECT `+chatCols+` FROM chats WHERE jid = ?`, k))
	return r, err == nil
}

func ensureChat(ex execer, k ChatKey) error {
	_, err := ex.Exec(`INSERT OR IGNORE INTO chats (jid) VALUES (?)`, k)
	return err
}

// chatColumns are the chat columns setChat may write.
var chatColumns = []string{"name", "unread", "pinned", "muted_until", "archived"}

func setChat(ex execer, k ChatKey, col string, v any) error {
	if !slices.Contains(chatColumns, col) {
		return fmt.Errorf("unknown chat column %q", col)
	}
	if err := ensureChat(ex, k); err != nil {
		return err
	}
	_, err := ex.Exec(`UPDATE chats SET `+col+` = ? WHERE jid = ?`, v, k)
	return err
}

func bumpChat(ex execer, k ChatKey, ts int64, preview string) error {
	_, err := ex.Exec(`UPDATE chats SET last_ts = ?, preview = ? WHERE jid = ? AND last_ts <= ?`, ts, preview, k, ts)
	return err
}

func addUnread(ex execer, k ChatKey) error {
	_, err := ex.Exec(`UPDATE chats SET unread = unread + 1 WHERE jid = ?`, k)
	return err
}

// insertMessage stores m unless it is already known, and reports whether it was new.
func insertMessage(ex execer, m Message) (bool, error) {
	var w, h int
	var thumb, media []byte
	if p := m.Photo; p != nil {
		w, h, thumb, media = p.W, p.H, p.Thumb, p.media
	}
	res, err := ex.Exec(`INSERT OR IGNORE INTO messages
		(chat, id, sender, sender_jid, from_me, ts, text, reply_to, edited, service, photo_w, photo_h, thumb, media)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Peer, m.ID, m.SenderID, m.senderJID, m.Out, m.Date.Unix(), m.Text, m.ReplyTo, m.Edited, m.Service, w, h, thumb, media)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func editMessage(ex execer, k ChatKey, id, text string) error {
	_, err := ex.Exec(`UPDATE messages SET text = ?, edited = 1 WHERE chat = ? AND id = ?`, text, k, id)
	return err
}

func revokeMessage(ex execer, k ChatKey, id string) error {
	_, err := ex.Exec(`UPDATE messages SET text = 'deleted this message', service = 1, reply_to = '',
		photo_w = 0, photo_h = 0, thumb = NULL, media = NULL WHERE chat = ? AND id = ?`, k, id)
	if err == nil {
		_, err = ex.Exec(`DELETE FROM reactions WHERE chat = ? AND msg = ?`, k, id)
	}
	return err
}

// setReaction records a reactor's reaction; an empty emoji removes it.
func setReaction(ex execer, k ChatKey, msg, reactor, emoji string, mine bool) error {
	if emoji == "" {
		_, err := ex.Exec(`DELETE FROM reactions WHERE chat = ? AND msg = ? AND reactor = ?`, k, msg, reactor)
		return err
	}
	_, err := ex.Exec(`INSERT OR REPLACE INTO reactions (chat, msg, reactor, emoji, mine) VALUES (?, ?, ?, ?, ?)`,
		k, msg, reactor, emoji, mine)
	return err
}

const msgCols = `id, sender, sender_jid, from_me, ts, text, reply_to, edited, service, photo_w, photo_h, thumb, media`

func scanMessages(k ChatKey, rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m := Message{Peer: k}
		var ts int64
		var w, h int
		var thumb, media []byte
		if err := rows.Scan(&m.ID, &m.SenderID, &m.senderJID, &m.Out, &ts, &m.Text, &m.ReplyTo,
			&m.Edited, &m.Service, &w, &h, &thumb, &media); err != nil {
			return nil, err
		}
		m.Date = time.Unix(ts, 0)
		if media != nil {
			m.Photo = &Photo{ID: m.ID, W: w, H: h, Thumb: thumb, media: media}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// page returns up to limit messages before the given one (or the latest),
// oldest first, with reactions attached.
func (d *db) page(k ChatKey, before *Message, limit int) ([]Message, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if before == nil {
		rows, err = d.Query(`SELECT `+msgCols+` FROM messages WHERE chat = ?
			ORDER BY ts DESC, id DESC LIMIT ?`, k, limit)
	} else {
		ts := before.Date.Unix()
		rows, err = d.Query(`SELECT `+msgCols+` FROM messages WHERE chat = ? AND (ts < ? OR (ts = ? AND id < ?))
			ORDER BY ts DESC, id DESC LIMIT ?`, k, ts, ts, before.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	ms, err := scanMessages(k, rows)
	if err != nil {
		return nil, err
	}
	slices.Reverse(ms)
	return ms, d.attachReactions(k, ms)
}

func (d *db) message(k ChatKey, id string) (Message, bool) {
	rows, err := d.Query(`SELECT `+msgCols+` FROM messages WHERE chat = ? AND id = ?`, k, id)
	if err != nil {
		return Message{}, false
	}
	ms, err := scanMessages(k, rows)
	if err != nil || len(ms) == 0 {
		return Message{}, false
	}
	if d.attachReactions(k, ms) != nil {
		return Message{}, false
	}
	return ms[0], true
}

func (d *db) attachReactions(k ChatKey, ms []Message) error {
	if len(ms) == 0 {
		return nil
	}
	args := []any{k}
	for _, m := range ms {
		args = append(args, m.ID)
	}
	rows, err := d.Query(`SELECT msg, emoji, COUNT(*), MAX(mine) FROM reactions
		WHERE chat = ? AND msg IN (?`+strings.Repeat(", ?", len(ms)-1)+`)
		GROUP BY msg, emoji ORDER BY MIN(rowid)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string][]Reaction{}
	for rows.Next() {
		var id string
		var r Reaction
		if err := rows.Scan(&id, &r.Emoji, &r.Count, &r.Mine); err != nil {
			return err
		}
		byID[id] = append(byID[id], r)
	}
	for i := range ms {
		ms[i].Reactions = byID[ms[i].ID]
	}
	return rows.Err()
}
