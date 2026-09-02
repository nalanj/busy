package web

import (
	"encoding/json"
	"fmt"
	"sync"
)

// SSEHub manages Server-Sent Events connections
type SSEHub struct {
	mu         sync.RWMutex
	clients    map[chan string]struct{}
	register   chan chan string
	unregister chan chan string
}

// NewSSEHub creates a new SSE event hub
func NewSSEHub() *SSEHub {
	hub := &SSEHub{
		clients:    make(map[chan string]struct{}),
		register:   make(chan chan string),
		unregister: make(chan chan string),
	}
	go hub.run()
	return hub
}

func (h *SSEHub) run() {
	for {
		select {
		case ch := <-h.register:
			h.mu.Lock()
			h.clients[ch] = struct{}{}
			h.mu.Unlock()

		case ch := <-h.unregister:
			h.mu.Lock()
			delete(h.clients, ch)
			close(ch)
			h.mu.Unlock()
		}
	}
}

// Subscribe returns a channel that receives SSE-formatted messages
func (h *SSEHub) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 100) // buffered to prevent blocking
	h.register <- ch

	cleanup := func() {
		h.unregister <- ch
	}

	return ch, cleanup
}

// Event represents an SSE event
type Event struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content"`
}

// Emit sends an event to all connected clients
func (h *SSEHub) Emit(eventType string, data interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	event := Event{Type: eventType, Content: data}
	jsonData, err := json.Marshal(event)
	if err != nil {
		return
	}

	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData))

	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			// Channel full, skip this client
		}
	}
}

// ClientCount returns the number of connected clients
func (h *SSEHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// GlobalEmitter is a package-level event emitter that can be set
var GlobalEmitter interface {
	Emit(eventType string, data interface{})
}

// SetGlobalEmitter sets the global event emitter
func SetGlobalEmitter(e interface{ Emit(eventType string, data interface{}) }) {
	GlobalEmitter = e
}

// EmitGlobal emits an event through the global emitter if set
func EmitGlobal(eventType string, data interface{}) {
	if GlobalEmitter != nil {
		GlobalEmitter.Emit(eventType, data)
	}
}
