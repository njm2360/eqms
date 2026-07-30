package core

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
)

// Hub は SSE の購読者全員へ同じフレームを配る。
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[chan []byte]struct{})}
}

func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// Publish は詰まっている購読者へのフレームを捨てる。周期的に送り直すものだけに使う。
func (h *Hub) Publish(event string, v any) { h.publish(event, v, false) }

// PublishKeep は捨てずに、詰まっていれば最も古いフレームを押し出して入れる。
// 一度しか送らない状態変化 (eqevent, deverr) に使う。
func (h *Hub) PublishKeep(event string, v any) { h.publish(event, v, true) }

func (h *Hub) publish(event string, v any, keep bool) {
	frame, err := Frame(event, v)
	if err != nil {
		log.Printf("hub: frame %s: %v", event, err)
		return
	}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- frame:
			continue
		default:
		}
		if !keep {
			continue
		}
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- frame:
		default:
		}
	}
	h.mu.Unlock()
}

func Frame(event string, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes(), nil
}
