package server

import (
	"net/http"
	"sync"
	"time"
)

// hub fans "the board changed" notifications out to every open board in a
// household. Each subscriber has a one-slot channel, so bursts of changes
// coalesce into a single refresh and a slow client never blocks a publisher.
type hub struct {
	mu     sync.Mutex
	subs   map[int64]map[chan struct{}]string // channel → client id
	closed chan struct{}
	once   sync.Once
}

func newHub() *hub {
	return &hub{subs: map[int64]map[chan struct{}]string{}, closed: make(chan struct{})}
}

// subscribe registers a listener for householdID. client identifies the
// page (see publish); it may be empty. Call the returned func to
// unsubscribe.
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

// publish tells every board in householdID to refresh, except the page
// whose client id is from (it already has the fresh board in its response).
func (h *hub) publish(householdID int64, from string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, client := range h.subs[householdID] {
		if from != "" && client == from {
			continue
		}
		select {
		case ch <- struct{}{}:
		default: // a refresh is already queued
		}
	}
}

// close ends every open stream (on server shutdown).
func (h *hub) close() { h.once.Do(func() { close(h.closed) }) }

// pingInterval keeps idle streams alive through proxies and lets the server
// notice clients that have gone away.
const pingInterval = 25 * time.Second

// boardClientHeader carries the page's random client id on board actions so
// the page isn't sent an event for its own change.
const boardClientHeader = "X-Board-Client"

// boardClient sanitises a client id: short and alphanumeric, or "".
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

// CloseStreams ends all open board event streams so a graceful shutdown
// isn't held up by them. Register it with http.Server.RegisterOnShutdown.
func (s *Server) CloseStreams() { s.events.close() }

// handleBoardEvents streams server-sent events: "board" whenever the board
// may have changed (the page re-fetches /board), and "lock" with "locked"
// or "unlocked", first on connect and then whenever the displays lock or
// unlock.
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
	// lockEvent says whether the displays are locked; the page compares it
	// with what it shows and reloads if they differ (so a reconnect catches
	// a missed change).
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

	// Tell the client how long to wait before reconnecting, and flush the
	// headers so EventSource fires "open" straight away.
	if _, err := w.Write([]byte("retry: 3000\n\n" + lockEvent())); err != nil {
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
