package server

import (
	"net/http"
	"sync"
	"time"
)

type hub struct {
	mu     sync.Mutex
	subs   map[int64]map[chan struct{}]string
	closed chan struct{}
	once   sync.Once
}

func newHub() *hub {
	return &hub{subs: map[int64]map[chan struct{}]string{}, closed: make(chan struct{})}
}

func (h *hub) subscribe(householdID int64, client string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[householdID] == nil {
		h.subs[householdID] = map[chan struct{}]string{}
	}
	h.subs[householdID][ch] = client
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[householdID], ch)
		h.mu.Unlock()
	}
}

func (h *hub) publish(householdID int64, from string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, client := range h.subs[householdID] {
		if from != "" && client == from {
			continue
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *hub) close() { h.once.Do(func() { close(h.closed) }) }

const pingInterval = 25 * time.Second

const boardClientHeader = "X-Board-Client"

func boardClient(v string) string {
	if len(v) == 0 || len(v) > 64 {
		return ""
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return ""
		}
	}
	return v
}

// CloseStreams ends all open board event streams.
func (s *Server) CloseStreams() { s.events.close() }

func (s *Server) handleBoardEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The stream is long-lived; don't let a server write timeout cut it.
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer
	w.WriteHeader(http.StatusOK)

	ch, unsubscribe := s.events.subscribe(hhOf(r), boardClient(r.URL.Query().Get("client")))
	defer unsubscribe()
	lockCh, unsubscribeLock := s.lockEvents.subscribe(hhOf(r), "")
	defer unsubscribeLock()
	lockEvent := func() string {
		st, err := s.locked(r)
		if err != nil {
			return ""
		}
		if st.Locked {
			return "event: lock\ndata: locked\n\n"
		}
		return "event: lock\ndata: unlocked\n\n"
	}

	// The version first: a display that sees it change reloads when idle.
	if _, err := w.Write([]byte("retry: 3000\n\nevent: version\ndata: " + s.version() + "\n\n" + lockEvent())); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		var msg string
		select {
		case <-r.Context().Done():
			return
		case <-s.events.closed:
			return
		case <-ch:
			msg = "event: board\ndata: changed\n\n"
		case <-lockCh:
			msg = lockEvent()
		case <-ping.C:
			msg = ": ping\n\n"
		}
		if _, err := w.Write([]byte(msg)); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
