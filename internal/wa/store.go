package wa

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ChatKey is a chat's JID in string form, without device, with LIDs mapped
// to phone numbers where the mapping is known.
type ChatKey string

func (k ChatKey) Zero() bool { return k == "" }

func (k ChatKey) IsGroup() bool { return strings.HasSuffix(string(k), "@"+types.GroupServer) }

func (k ChatKey) jid() types.JID {
	j, _ := types.ParseJID(string(k))
	return j
}

type Chat struct {
	Key        ChatKey
	Title      string
	Unread     int
	Preview    string
	LastDate   int
	Pinned     bool
	MutedUntil int64
	Archived   bool
}

func (c Chat) Group() bool { return c.Key.IsGroup() }

func (c Chat) Muted() bool { return c.MutedUntil > time.Now().Unix() }

type Message struct {
	ID        string
	Peer      ChatKey
	SenderID  string
	Sender    string
	Out       bool
	Date      time.Time
	Text      string
	ReplyTo   string
	Edited    bool
	Service   bool
	Photo     *Photo
	Reactions []Reaction

	// senderJID is the sender as addressed on the wire, which message keys
	// for replies and reactions must use.
	senderJID string
}

// Photo is an image message. Thumb is the low-resolution JPEG embedded in
// the message; the full image is downloaded from media.
type Photo struct {
	ID    string
	W, H  int
	Thumb []byte
	media []byte
}

type Reaction struct {
	Emoji string
	Count int
	Mine  bool
}

// Summary is a one-line, text-only rendering of the message.
func (m Message) Summary() string {
	if m.Photo == nil {
		return m.Text
	}
	if m.Text == "" {
		return "[photo]"
	}
	return "[photo] " + m.Text
}

func less(a, b Message) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.Before(b.Date)
	}
	return a.ID < b.ID
}

type AuthState int

const (
	AuthConnecting AuthState = iota
	AuthQR
	AuthCode
	AuthReady
	AuthFailed
)

type Auth struct {
	State     AuthState
	QRURL     string
	QRExpires time.Time
	Code      string
	Error     string
}

// Store holds everything the UI renders. All access goes through its mutex;
// readers get copies.
type Store struct {
	mu       sync.RWMutex
	onChange func()
	gen      atomic.Uint64 // bumped on every change; UI refreshes only when it moves

	auth    Auth
	online  bool
	status  string
	statusT time.Time

	chats    map[ChatKey]*Chat
	messages map[ChatKey][]Message
	loading  map[ChatKey]bool
	loaded   map[ChatKey]bool
	complete map[ChatKey]bool
}

func NewStore(onChange func()) *Store {
	return &Store{
		onChange: onChange,
		chats:    map[ChatKey]*Chat{},
		messages: map[ChatKey][]Message{},
		loading:  map[ChatKey]bool{},
		loaded:   map[ChatKey]bool{},
		complete: map[ChatKey]bool{},
	}
}

func (s *Store) changed() {
	s.gen.Add(1)
	if s.onChange != nil {
		s.onChange()
	}
}

// Gen is a monotonic counter of store mutations. The UI uses it to avoid
// copying chats/messages on every Gio frame (which stalls Wayland pongs).
func (s *Store) Gen() uint64 { return s.gen.Load() }

func (s *Store) update(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
	s.changed()
}

func (s *Store) Auth() Auth {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.auth
}

func (s *Store) setAuth(a Auth) { s.update(func() { s.auth = a }) }

func (s *Store) Online() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.online
}

func (s *Store) setOnline(v bool) { s.update(func() { s.online = v }) }

// Status returns a transient status/error line, if one is still fresh.
func (s *Store) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if time.Since(s.statusT) > 6*time.Second {
		return ""
	}
	return s.status
}

func (s *Store) SetStatus(msg string) {
	s.update(func() { s.status, s.statusT = msg, time.Now() })
}

// Chats returns all unarchived chats with any activity, pinned first, then
// by recency.
func (s *Store) Chats() []Chat {
	s.mu.RLock()
	out := make([]Chat, 0, len(s.chats))
	for _, c := range s.chats {
		if !c.Archived && (c.LastDate > 0 || c.Pinned) {
			out = append(out, *c)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		if out[i].LastDate != out[j].LastDate {
			return out[i].LastDate > out[j].LastDate
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (s *Store) Chat(k ChatKey) (Chat, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.chats[k]
	if !ok {
		return Chat{}, false
	}
	return *c, true
}

func (s *Store) Messages(k ChatKey) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Message(nil), s.messages[k]...)
}

func (s *Store) Message(k ChatKey, id string) (Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findLocked(k, id)
}

func (s *Store) HistoryState(k ChatKey) (loaded, loading, complete bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded[k], s.loading[k], s.complete[k]
}

func (s *Store) TotalUnread() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, c := range s.chats {
		if !c.Muted() && !c.Archived && (c.LastDate > 0 || c.Pinned) {
			n += c.Unread
		}
	}
	return n
}

func (s *Store) findLocked(k ChatKey, id string) (Message, bool) {
	for _, m := range s.messages[k] {
		if m.ID == id {
			return m, true
		}
	}
	return Message{}, false
}

// upsertLocked inserts or replaces messages, keeping them sorted by date.
func (s *Store) upsertLocked(k ChatKey, msgs ...Message) {
	ms := s.messages[k]
	for _, m := range msgs {
		if i := indexOf(ms, m.ID); i >= 0 {
			ms = append(ms[:i], ms[i+1:]...)
		}
		i := sort.Search(len(ms), func(i int) bool { return less(m, ms[i]) })
		ms = append(ms, Message{})
		copy(ms[i+1:], ms[i:])
		ms[i] = m
	}
	s.messages[k] = ms
}

func indexOf(ms []Message, id string) int {
	for i := range ms {
		if ms[i].ID == id {
			return i
		}
	}
	return -1
}
