package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one message pushed to connected dashboards.
type Event struct {
	// Type names the event so the client can route it without inspecting the
	// payload: "incident", "explanation", "health", "hello".
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Broker fans one stream of events out to every connected dashboard.
//
// Server-Sent Events rather than WebSockets: the traffic is strictly one-way,
// SSE reconnects on its own without a client library, and it survives proxies
// that would need explicit configuration to pass a WebSocket upgrade. Nothing
// here needs a duplex channel.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
	// buffer bounds each subscriber's queue. A dashboard on a slow connection
	// must not be able to stall the detector by refusing to read.
	buffer int
}

// NewBroker builds a broker.
func NewBroker() *Broker {
	return &Broker{subscribers: make(map[chan Event]struct{}), buffer: 64}
}

// Subscribe registers a listener and returns it with its unsubscribe function.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, b.buffer)

	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
}

// Publish delivers an event to every subscriber.
//
// A subscriber whose buffer is full is skipped rather than waited on. The
// alternative is one stalled browser tab blocking the detector for every other
// viewer, which trades a dropped update for a frozen product.
func (b *Broker) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// Subscribers returns how many dashboards are connected.
func (b *Broker) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// heartbeatInterval keeps idle connections alive.
//
// Proxies and load balancers close a connection that has been silent for a
// minute or so, and an SSE stream in a healthy cluster is silent by design —
// no incidents is the good outcome. The comment frames cost nothing and stop
// the dashboard from flapping between connected and disconnected.
const heartbeatInterval = 20 * time.Second

// streamHandler serves the SSE endpoint.
func (s *Server) streamHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Nginx buffers proxied responses by default, which turns a live stream
	// into a batch delivered when the connection closes.
	w.Header().Set("X-Accel-Buffering", "no")
	s.applyCORS(w, r)

	events, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()

	// The first message tells the client it is connected, so the UI can show a
	// live indicator without waiting for a cluster to break.
	writeSSE(w, Event{Type: "hello", Data: map[string]any{
		"source":      s.sourceName,
		"mode":        string(s.cfg.Mode),
		"connected":   true,
		"subscribers": s.broker.Subscribers(),
	}})
	flusher.Flush()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case event, open := <-events:
			if !open {
				return
			}
			if !writeSSE(w, event) {
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			// An SSE comment: ignored by the client, but enough traffic to keep
			// intermediaries from closing the connection.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE encodes one event in the text/event-stream format.
func writeSSE(w http.ResponseWriter, event Event) bool {
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return false
	}

	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
		return false
	}
	return true
}
