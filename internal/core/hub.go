package core

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
)

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

func (h *Hub) Publish(event string, v any) { h.publish(event, v, false) }

// 詰まった購読者に最古フレームを押し出して届ける。一度しか送らないイベント(eqevent)用
func (h *Hub) PublishKeep(event string, v any) { h.publish(event, v, true) }

func (h *Hub) PublishRaw(event string, data []byte) {
	h.fanout(rawFrame(event, data), false)
}

func (h *Hub) publish(event string, v any, keep bool) {
	frame, err := Frame(event, v)
	if err != nil {
		log.Printf("hub: frame %s: %v", event, err)
		return
	}
	h.fanout(frame, keep)
}

func (h *Hub) fanout(frame []byte, keep bool) {
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
	return rawFrame(event, data), nil
}

func rawFrame(event string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}
