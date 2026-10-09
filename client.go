package main

// Every client (including the host's) does the same thing each tick:
//  1. notice if the local user paused/resumed/seeked -> tell the room
//  2. apply changes other people made
//  3. otherwise correct slow drift against the room

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pollInterval       = 500 * time.Millisecond
	driftSeconds       = 1.5 // resync to the room if this far off while playing
	pausedDriftSeconds = 0.3 // tighter while paused so everyone sits on the same frame
	seekJumpSeconds    = 1.0 // a sudden position change bigger than this is the user seeking
	seekCooldown       = 3 * time.Second
	syncDelay          = 1500 * time.Millisecond // after a pause/resume/seek, let the stream settle, then align onboards + live timing
	echoTimeout        = 3 * time.Second         // give up waiting for the room to confirm our own change
	reconnectDelay     = 3 * time.Second
)

type acceptedState struct {
	stateMsg
	recvAt time.Time
	own    bool
}

type sample struct {
	paused bool
	time   float64
	at     time.Time
}

type client struct {
	url, name, password, hostToken, playerID string
	isHost                                   bool

	mu            sync.Mutex // guards everything below; held for a whole tick so state can't change mid-way
	conn          *websocket.Conn
	wmu           sync.Mutex
	myID          int64 // our connection id, assigned by the server (0 = none yet)
	seq, ackedSeq int64 // our last "set" / the last one the room confirmed
	sentAt        time.Time
	room          *acceptedState // accepted room state
	latest        *stateMsg      // newest state from the server, even if we're ignoring it for now
	appliedRev    int64
	appliedPaused *bool // paused state of the last room state we applied (ours or someone else's)
	last          *sample
	cooldownUntil time.Time
	syncAt        time.Time // when to next call MultiViewer's sync (zero = none due)
	lastHeard     atomic.Int64

	warnedPlayer, warnedSession string
	warnedMV, warnedLive        bool
}

func runClient(url, name, password, hostToken, playerID string) {
	c := &client{url: url, name: name, password: password, hostToken: hostToken, playerID: playerID, isHost: hostToken != "", appliedRev: -1}
	go c.connectLoop()
	for {
		time.Sleep(pollInterval)
		if err := c.tick(); err != nil {
			c.mu.Lock()
			if !c.warnedMV {
				msg := err.Error()
				if strings.Contains(msg, "Client.Timeout") {
					msg = "no response"
				}
				logf("MultiViewer not reachable: %s", msg)
			}
			c.warnedMV = true
			c.last = nil
			c.mu.Unlock()
		}
	}
}

func (c *client) waitingForEcho() bool { return c.seq > c.ackedSeq }

func expected(r *acceptedState, now time.Time) float64 {
	if r.Paused {
		return r.Time
	}
	return r.Time + now.Sub(r.recvAt).Seconds()
}

func fmtTime(s float64) string {
	return fmt.Sprintf("%d:%02d", int(s/60), int(math.Mod(s, 60)))
}

func (c *client) accept(s *stateMsg) {
	c.room = &acceptedState{stateMsg: *s, recvAt: time.Now(), own: s.From == c.myID}
}

func (c *client) sendSet(p *player, now time.Time) bool {
	if c.conn == nil {
		return false
	}
	c.seq++
	c.sentAt = now
	msg := map[string]any{"type": "set", "paused": p.Paused, "time": p.Time, "seq": c.seq, "by": c.name, "sessionKey": nil}
	if p.SessionKey != "" {
		msg["sessionKey"] = p.SessionKey
	}
	b, _ := json.Marshal(msg)
	c.wmu.Lock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	c.conn.WriteMessage(websocket.TextMessage, b)
	c.wmu.Unlock()
	c.appliedRev = -1 // whatever the room said before is superseded by this
	if c.room != nil {
		c.appliedRev = c.room.Rev
	}
	paused := p.Paused
	c.appliedPaused = &paused
	return true
}

func (c *client) connectLoop() {
	warnedUnreachable := false
	for {
		header := http.Header{}
		if c.password != "" {
			header.Set("X-Room-Password", c.password)
		}
		if c.hostToken != "" {
			header.Set("X-Host-Token", c.hostToken)
		}
		dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
		conn, res, err := dialer.Dial(c.url, header)
		if err != nil {
			if res != nil && res.StatusCode == http.StatusUnauthorized {
				fatal("Wrong room password.")
			}
			if !warnedUnreachable {
				logf("Can't reach %s yet - is the host running? Retrying every %ds...", c.url, int(reconnectDelay.Seconds()))
				warnedUnreachable = true
			}
			time.Sleep(reconnectDelay)
			continue
		}
		warnedUnreachable = false
		conn.SetReadLimit(64 * 1024)
		c.lastHeard.Store(time.Now().UnixMilli())
		conn.SetPingHandler(func(data string) error {
			c.lastHeard.Store(time.Now().UnixMilli())
			return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
		})

		c.mu.Lock()
		c.conn = conn
		c.myID = 0
		c.seq, c.ackedSeq = 0, 0
		c.appliedRev = -1
		c.appliedPaused = nil
		c.mu.Unlock()
		if !c.isHost {
			logf("Connected to %s", c.url)
		}

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			c.lastHeard.Store(time.Now().UnixMilli())
			c.handle(data)
		}

		conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.myID = 0
		c.mu.Unlock()
		if c.isHost {
			logf("Lost connection to the room, reconnecting...")
		} else {
			logf("Lost connection to host, reconnecting...")
		}
		time.Sleep(reconnectDelay)
	}
}

func (c *client) handle(data []byte) {
	var head struct {
		Type string `json:"type"`
		ID   *int64 `json:"id"`
	}
	if json.Unmarshal(data, &head) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if head.Type == "hello" && head.ID != nil {
		c.myID = *head.ID
		return
	}
	s := parseState(data)
	if s == nil {
		return
	}
	c.latest = s
	if s.From == c.myID {
		// echo of our own change; an older echo is already superseded by a newer one in flight
		if s.Seq >= c.seq {
			c.ackedSeq = s.Seq
			c.accept(s)
		}
	} else if !c.waitingForEcho() {
		// while our own change is in flight, anything earlier from others is superseded by it
		c.accept(s)
	}
}

func (c *client) announce() {
	r := c.room
	switch {
	case c.appliedPaused == nil:
		state := "playing"
		if r.Paused {
			state = "paused"
		}
		logf("Joined the room (%s at %s)", state, fmtTime(r.Time))
	case *c.appliedPaused != r.Paused:
		verb := "resumed"
		if r.Paused {
			verb = "paused"
		}
		logf("%s %s at %s", r.By, verb, fmtTime(r.Time))
	default:
		logf("%s jumped to %s", r.By, fmtTime(r.Time))
	}
}

func (c *client) pickPlayer(all []player) *player {
	for i := range all {
		if c.playerID != "" && all[i].ID == c.playerID {
			return &all[i]
		}
	}
	if c.playerID != "" {
		return nil
	}
	for i := range all {
		if !all[i].Onboard {
			return &all[i]
		}
	}
	if len(all) > 0 {
		return &all[0]
	}
	return nil
}

// reconcile makes the local player match the room. remoteChange = someone just paused/seeked on purpose:
// apply it right away. Otherwise this is drift correction, which backs off after a seek.
func (c *client) reconcile(p *player, all []player, now time.Time, remoteChange bool) error {
	target := c.room
	acted := false
	t := p.Time
	if p.Paused != target.Paused {
		// pause/resume every open window (main feed and onboards), not just the followed one
		for _, x := range all {
			if x.Paused != target.Paused {
				if err := setPaused(x.ID, target.Paused); err != nil {
					return err
				}
			}
		}
		acted = true
	}
	exp := expected(target, now)
	drift := p.Time - exp
	limit := driftSeconds
	if target.Paused {
		limit = pausedDriftSeconds
	}
	if math.Abs(drift) > limit && (remoteChange || !now.Before(c.cooldownUntil)) {
		if err := seekTo(p.ID, exp); err != nil {
			return err
		}
		t = exp
		c.cooldownUntil = now.Add(seekCooldown)
		sign := ""
		if drift > 0 {
			sign = "+"
		}
		logf("Resynced to %s (was %s%.1fs off)", fmtTime(exp), sign, drift)
		acted = true
	}
	if acted {
		c.syncAt = now.Add(syncDelay)
	}
	// Remember what the player looks like now (after our own changes), so our changes aren't
	// mistaken for the user's, while a user action right after them is still noticed.
	c.last = &sample{paused: target.Paused, time: t, at: now}
	return nil
}

func (c *client) tick() error {
	all, err := getPlayers()
	if err != nil {
		return err
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warnedMV = false

	// A connection that went quiet is dead even if the OS hasn't noticed yet.
	if c.conn != nil && now.Sub(time.UnixMilli(c.lastHeard.Load())) > heartbeatInterval*5/2 {
		c.conn.Close()
	}

	p := c.pickPlayer(all)
	if p == nil {
		msg := "No MultiViewer player open - open the replay."
		if len(all) > 0 {
			ids := make([]string, len(all))
			for i, x := range all {
				ids[i] = fmt.Sprintf("%s (%s)", x.ID, x.Label)
			}
			msg = fmt.Sprintf("Player %s not found. Open players: %s", c.playerID, strings.Join(ids, ", "))
		}
		if c.warnedPlayer != msg {
			logf("%s", msg)
		}
		c.warnedPlayer = msg
		c.last = nil
		return nil
	}
	c.warnedPlayer = ""

	if p.Live && !c.warnedLive {
		logf("This is a live stream. Only replays can be synced (live streams can't be seeked).")
		c.warnedLive = true
	}

	// The room didn't confirm our change in time (dropped, or connection trouble): follow whatever it says now.
	if c.waitingForEcho() && now.Sub(c.sentAt) > echoTimeout {
		c.ackedSeq = c.seq
		if c.latest != nil {
			c.accept(c.latest)
		}
	}

	// First client in the room (the host) seeds it.
	if c.room == nil {
		if c.isHost && !c.waitingForEcho() {
			c.sendSet(p, now)
		}
		c.last = &sample{paused: p.Paused, time: p.Time, at: now}
		return nil
	}

	if c.room.SessionKey != nil && p.SessionKey != "" && *c.room.SessionKey != p.SessionKey {
		if c.isHost {
			if !c.waitingForEcho() {
				c.sendSet(p, now) // the host switched replays: take the room along
			}
		} else if c.warnedSession != p.SessionKey {
			logf("You have a different session open (%s) than the room (%s). Not syncing.", p.SessionKey, *c.room.SessionKey)
			c.warnedSession = p.SessionKey
		}
		c.last = nil
		return nil
	}
	c.warnedSession = ""

	// Time to line up the onboards and live timing with the main feed?
	if !c.syncAt.IsZero() && !now.Before(c.syncAt) {
		c.syncAt = time.Time{}
		if err := syncOthersTo(p.ID); err != nil {
			return err
		}
	}

	// 1. Did the local user do something? (Checked every tick, so quick repeated seeks all count.)
	if c.last != nil {
		wall := now.Sub(c.last.at).Seconds()
		delta := p.Time - c.last.time
		pausedFlip := p.Paused != c.last.paused
		jumped := false
		if p.Paused == c.last.paused {
			if p.Paused {
				jumped = math.Abs(delta) > 0.5
			} else {
				jumped = math.Abs(delta-wall) > seekJumpSeconds
			}
		}
		if (pausedFlip || jumped) && c.sendSet(p, now) {
			switch {
			case pausedFlip && p.Paused:
				logf("You paused")
			case pausedFlip:
				logf("You resumed")
			default:
				logf("You jumped to %s", fmtTime(p.Time))
			}
			if pausedFlip { // carry a pause/resume over to your own onboard windows too
				for _, x := range all {
					if x.Paused != p.Paused {
						if err := setPaused(x.ID, p.Paused); err != nil {
							return err
						}
					}
				}
			}
			c.syncAt = now.Add(syncDelay) // keep your own onboards/timing lined up with what you just did
			c.last = &sample{paused: p.Paused, time: p.Time, at: now}
			return nil
		}
	}

	// 2. Someone else changed something.
	if c.room.Rev != c.appliedRev {
		c.appliedRev = c.room.Rev
		if !c.room.own {
			c.announce()
		}
		paused := c.room.Paused
		c.appliedPaused = &paused
		if !c.room.own {
			return c.reconcile(p, all, now, true)
		}
	}

	// 3. Slow drift. Not while our own change is still on its way to the room.
	if c.waitingForEcho() {
		c.last = &sample{paused: p.Paused, time: p.Time, at: now}
		return nil
	}
	return c.reconcile(p, all, now, false)
}
