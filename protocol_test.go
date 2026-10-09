package main

import "testing"

func TestRoomURL(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4":      "ws://1.2.3.4:8765",
		"1.2.3.4:99":   "ws://1.2.3.4:99",
		"fd00::1":      "ws://[fd00::1]:8765",
		"[fd00::1]:99": "ws://[fd00::1]:99",
		"[fd00::1]":    "ws://[fd00::1]:8765",
		" pc.local ":   "ws://pc.local:8765",
	} {
		if got := roomURL(in); got != want {
			t.Errorf("roomURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"anna":                "anna",
		"\x1b[2J\x1b[31mevil": "[2J[31mevil",
		"  ":                  "someone",
		"0123456789012345678901234567890123456789extra": "0123456789012345678901234567890123456789",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSet(t *testing.T) {
	if parseSet([]byte(`{"type":"set","paused":true,"time":12.5,"sessionKey":"11253","seq":1,"by":"x"}`)) == nil {
		t.Error("valid set rejected")
	}
	for _, bad := range []string{
		`{"type":"set","paused":true,"time":"1) } mutation { x","seq":1}`, // injection attempt
		`{"type":"set","paused":"yes","time":1,"seq":1}`,
		`{"type":"set","paused":true,"time":-1,"seq":1}`,
		`{"type":"set","paused":true,"time":1,"seq":0}`,
		`{"type":"set","paused":true,"time":1,"seq":1,"sessionKey":"1\") }"}`,
		`{"type":"state","paused":true,"time":1,"seq":1}`,
		`not json`,
	} {
		if parseSet([]byte(bad)) != nil {
			t.Errorf("accepted invalid set: %s", bad)
		}
	}
}
