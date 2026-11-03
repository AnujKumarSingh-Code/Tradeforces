package server

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type client struct {
	conn   *websocket.Conn
	player *Player
	out    chan []byte
	once   sync.Once
}

func (c *client) send(b []byte) {
	select {
	case c.out <- b:
	default:
		// Slow client: drop the frame rather than stall the engine loop.
	}
}

func (c *client) close() {
	c.once.Do(func() { close(c.out) })
}

type hub struct {
	mu sync.Mutex
	cs map[*client]struct{}
}

func newHub() *hub { return &hub{cs: map[*client]struct{}{}} }

func (h *hub) add(c *client) {
	h.mu.Lock()
	h.cs[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(c *client) {
	h.mu.Lock()
	delete(h.cs, c)
	h.mu.Unlock()
	c.close()
}

func (h *hub) clients() []*client {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*client, 0, len(h.cs))
	for c := range h.cs {
		out = append(out, c)
	}
	return out
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	c := &client{conn: conn, player: p, out: make(chan []byte, 64)}
	s.hub.add(c)
	s.mu.Lock()
	eng, phase := s.eng, s.phase
	s.mu.Unlock()
	if p != nil && eng != nil && phase != "closed" {
		eng.Join(p.ID, p.Name)
	}

	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		defer conn.Close()
		for {
			select {
			case b, ok := <-c.out:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	}
	s.hub.remove(c)
}
