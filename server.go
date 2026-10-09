package main

// Room relay. Holds the single shared playback state; any participant may change it.
// Only the host's own client (which knows hostToken) may move the room to another session.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type peer struct {
	conn   *websocket.Conn
	id     int64
	isHost bool
	alive  atomic.Bool
	wmu    sync.Mutex // gorilla allows one writer at a time
}

func (p *peer) send(b []byte) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	p.conn.WriteMessage(websocket.TextMessage, b)
}

type room struct {
	paused     bool
	time       float64
	at         time.Time // while playing, `time` is the position at this moment
	sessionKey *string
	rev, from  int64
	seq        int64
	by         string
}

type server struct {
	mu        sync.Mutex
	room      *room
	peers     map[*peer]bool
	nextID    int64
	hostToken string
	pwHash    []byte // nil = no password
}

// startServer opens the room on port. It returns the token the host's own client uses to identify itself.
func startServer(port int, password string) (string, error) {
	tok := make([]byte, 16)
	rand.Read(tok)
	s := &server{peers: map[*peer]bool{}, hostToken: hex.EncodeToString(tok)}
	if password != "" {
		h := sha256.Sum256([]byte(password))
		s.pwHash = h[:]
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port)) // all interfaces, IPv4 and IPv6
	if err != nil {
		if isAddrInUse(err) {
			return "", fmt.Errorf("Port %d is already in use.", port)
		}
		return "", err
	}
	go http.Serve(ln, http.HandlerFunc(s.handle))
	go s.heartbeat()

	suffix := ""
	if password != "" {
		suffix = " (password protected)"
	}
	logf("Room open on port %d%s.", port, suffix)
	return s.hostToken, nil
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	// Browsers always send Origin; our clients never do. Refusing it stops a web page from joining a room.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if s.pwHash != nil {
		given := sha256.Sum256([]byte(r.Header.Get("X-Room-Password")))
		if subtle.ConstantTimeCompare(given[:], s.pwHash) != 1 {
			http.Error(w, "Wrong room password", http.StatusUnauthorized)
			return
		}
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxMessageBytes)

	p := &peer{conn: conn}
	p.isHost = subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Host-Token")), []byte(s.hostToken)) == 1
	p.alive.Store(true)
	conn.SetPongHandler(func(string) error { p.alive.Store(true); return nil })

	s.mu.Lock()
	s.nextID++
	p.id = s.nextID
	s.peers[p] = true
	count := len(s.peers)
	var snap []byte
	if s.room != nil {
		snap = s.snapshot()
	}
	s.mu.Unlock()

	if !p.isHost {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		logf("Someone connected (%s). %d in room.", host, count)
	}
	hello, _ := json.Marshal(map[string]any{"type": "hello", "id": p.id})
	p.send(hello)
	if snap != nil {
		p.send(snap)
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		m := parseSet(data)
		if m == nil {
			continue
		}
		s.mu.Lock()
		if s.room != nil && s.room.sessionKey != nil && m.SessionKey != nil && *s.room.sessionKey != *m.SessionKey && !p.isHost {
			s.mu.Unlock()
			continue
		}
		next := &room{paused: *m.Paused, time: *m.Time, at: time.Now(), sessionKey: m.SessionKey, from: p.id, seq: *m.Seq, by: m.By.(string)}
		if s.room != nil {
			next.rev = s.room.rev + 1
			if next.sessionKey == nil {
				next.sessionKey = s.room.sessionKey
			}
		} else {
			next.rev = 1
		}
		s.room = next
		snap := s.snapshot()
		targets := make([]*peer, 0, len(s.peers))
		for q := range s.peers {
			targets = append(targets, q)
		}
		s.mu.Unlock()
		for _, q := range targets {
			q.send(snap)
		}
	}

	conn.Close()
	s.mu.Lock()
	delete(s.peers, p)
	count = len(s.peers)
	s.mu.Unlock()
	if !p.isHost {
		logf("Someone left. %d in room.", count)
	}
}

// snapshot encodes the room state; while playing it hands out the current position. Caller holds s.mu.
func (s *server) snapshot() []byte {
	r := s.room
	t := r.time
	if !r.paused {
		t += time.Since(r.at).Seconds()
	}
	b, _ := json.Marshal(stateMsg{Type: "state", Paused: r.paused, Time: t, SessionKey: r.sessionKey, Rev: r.rev, From: r.from, Seq: r.seq, By: r.by})
	return b
}

// heartbeat drops connections that silently died (e.g. a ZeroTier peer vanished).
func (s *server) heartbeat() {
	for range time.Tick(heartbeatInterval) {
		s.mu.Lock()
		peers := make([]*peer, 0, len(s.peers))
		for p := range s.peers {
			peers = append(peers, p)
		}
		s.mu.Unlock()
		for _, p := range peers {
			if !p.alive.Swap(false) {
				p.conn.Close()
				continue
			}
			p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
		}
	}
}
