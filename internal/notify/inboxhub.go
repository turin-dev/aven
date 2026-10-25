package notify

import (
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// InboxItem은 사용자 알림함에 막 들어간 항목이다(GET /api/notifications/stream으로 보낸다).
// 알림함 항목 id는 저장소가 만들므로 담지 않는다. 받은 쪽은 event_id로 구분하거나 알림함을 다시 읽는다.
type InboxItem struct {
	EventID       string    `json:"event_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID *string   `json:"environment_id"`
	ServiceID     *string   `json:"service_id"`
	Kind          string    `json:"kind"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"created_at"`
}

// inboxBuffer는 구독자마다 쌓아 두는 항목 수다. 느린 구독자 때문에 Fanout이 막히지 않도록 넘치면 버린다.
const inboxBuffer = 32

// InboxHub는 사용자별 알림함 pub/sub이다. 영(0) 값을 바로 쓸 수 있다.
type InboxHub struct {
	mu   sync.Mutex
	subs map[string]map[chan InboxItem]struct{}
	kick chan struct{} // Kick이 닫고 새로 만든다
}

// Subscribe는 userID의 새 알림을 받는 채널과 해지 함수다.
func (h *InboxHub) Subscribe(userID string) (<-chan InboxItem, func()) {
	ch := make(chan InboxItem, inboxBuffer)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[string]map[chan InboxItem]struct{}{}
	}
	m := h.subs[userID]
	if m == nil {
		m = map[chan InboxItem]struct{}{}
		h.subs[userID] = m
	}
	m[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if m := h.subs[userID]; m != nil {
				delete(m, ch)
				if len(m) == 0 {
					delete(h.subs, userID)
				}
			}
			h.mu.Unlock()
		})
	}
}

// Publish는 userID의 구독자에게 항목을 보낸다(막히지 않는다).
func (h *InboxHub) Publish(userID string, it InboxItem) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[userID] {
		select {
		case ch <- it:
		default: // 가득 찼으면 버린다(웹은 폴링으로 따라잡는다)
		}
	}
}

// Subscribers는 userID의 구독자 수다(테스트용).
func (h *InboxHub) Subscribers(userID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[userID])
}

// Kicked는 다음 Kick 때 닫히는 채널이다. 스트림은 이것을 보고 자격(세션, 토큰)을 다시 검사한다.
func (h *InboxHub) Kicked() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.kick == nil {
		h.kick = make(chan struct{})
	}
	return h.kick
}

// Kick은 열린 스트림 모두에게 자격을 다시 검사하라고 알린다(로그아웃, 세션 끊기 뒤).
func (h *InboxHub) Kick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.kick != nil {
		close(h.kick)
	}
	h.kick = make(chan struct{})
}

func (h *InboxHub) publishEvent(e *store.Event, userIDs []string) {
	it := InboxItem{EventID: e.ID, ProjectID: e.ProjectID, EnvironmentID: e.EnvironmentID, ServiceID: e.ServiceID,
		Kind: e.Kind, Message: e.Message, CreatedAt: e.CreatedAt}
	for _, uid := range userIDs {
		h.Publish(uid, it)
	}
}

// Inbox는 Fanout이 새 알림함 항목을 내보내는 허브다.
func (n *Notifier) Inbox() *InboxHub { return &n.inbox }
