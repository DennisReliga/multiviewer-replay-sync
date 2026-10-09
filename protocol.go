package main

// Shared limits and validation for messages between clients and the room server.
//
//	server -> client  {"type":"hello","id":N}
//	client -> server  {"type":"set","paused":B,"time":T,"sessionKey":S,"seq":N,"by":NAME}
//	server -> client  {"type":"state","paused":B,"time":T,"sessionKey":S,"rev":N,"from":N,"seq":N,"by":NAME}
//
// `from`/`seq` let a client recognise the echo of its own change.

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultPort     = 8765
	maxMessageBytes = 4096
	maxTimeSeconds  = 7 * 24 * 3600 // nothing plausible is longer than this
)

var heartbeatInterval = func() time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("MVRS_HEARTBEAT_MS")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return 10 * time.Second
}()

var sessionKeyPattern = regexp.MustCompile(`^[\w-]{1,64}$`)

// cleanName strips control characters from a display name (no terminal escape tricks).
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > 40 {
		s = string([]rune(s)[:40])
	}
	if s == "" {
		return "someone"
	}
	return s
}

// roomURL turns "1.2.3.4", "1.2.3.4:99", "fd00::1", "[fd00::1]:99" or "host.name" into a ws:// URL.
func roomURL(target string) string {
	t := strings.TrimSpace(target)
	port := strconv.Itoa(defaultPort)
	if strings.HasPrefix(t, "[") {
		if regexp.MustCompile(`\]:\d+$`).MatchString(t) {
			return "ws://" + t
		}
		return "ws://" + t + ":" + port
	}
	switch strings.Count(t, ":") {
	case 0:
		return "ws://" + t + ":" + port
	case 1:
		return "ws://" + t
	default: // bare IPv6
		return "ws://[" + t + "]:" + port
	}
}

func validTime(t *float64) bool {
	return t != nil && !math.IsNaN(*t) && !math.IsInf(*t, 0) && *t >= 0 && *t <= maxTimeSeconds
}

func validSession(s *string) bool { return s == nil || sessionKeyPattern.MatchString(*s) }

type setMsg struct {
	Type       string   `json:"type"`
	Paused     *bool    `json:"paused"`
	Time       *float64 `json:"time"`
	SessionKey *string  `json:"sessionKey"`
	Seq        *int64   `json:"seq"`
	By         any      `json:"by"`
}

// stateMsg is what the server broadcasts; also what a client accepts after validation.
type stateMsg struct {
	Type       string  `json:"type"`
	Paused     bool    `json:"paused"`
	Time       float64 `json:"time"`
	SessionKey *string `json:"sessionKey"`
	Rev        int64   `json:"rev"`
	From       int64   `json:"from"`
	Seq        int64   `json:"seq"`
	By         string  `json:"by"`
}

func nameOf(v any) string {
	if s, ok := v.(string); ok {
		return cleanName(s)
	}
	return "someone"
}

// parseSet validates a "set" sent by a client. Returns nil if invalid.
func parseSet(data []byte) *setMsg {
	var m setMsg
	if json.Unmarshal(data, &m) != nil || m.Type != "set" {
		return nil
	}
	if m.Paused == nil || !validTime(m.Time) || !validSession(m.SessionKey) || m.Seq == nil || *m.Seq < 1 {
		return nil
	}
	m.By = nameOf(m.By)
	return &m
}

// parseState validates a "state" sent by the server. Returns nil if invalid.
func parseState(data []byte) *stateMsg {
	var m struct {
		Type       string   `json:"type"`
		Paused     *bool    `json:"paused"`
		Time       *float64 `json:"time"`
		SessionKey *string  `json:"sessionKey"`
		Rev        *int64   `json:"rev"`
		From       *int64   `json:"from"`
		Seq        *int64   `json:"seq"`
		By         any      `json:"by"`
	}
	if json.Unmarshal(data, &m) != nil || m.Type != "state" {
		return nil
	}
	if m.Paused == nil || !validTime(m.Time) || !validSession(m.SessionKey) || m.Rev == nil || m.From == nil || m.Seq == nil {
		return nil
	}
	return &stateMsg{
		Type: "state", Paused: *m.Paused, Time: *m.Time, SessionKey: m.SessionKey,
		Rev: *m.Rev, From: *m.From, Seq: *m.Seq, By: nameOf(m.By),
	}
}
