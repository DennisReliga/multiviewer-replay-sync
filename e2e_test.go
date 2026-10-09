package main

// End-to-end tests: real app processes (host + joiners) against fake MultiViewer players.
//
//	go test ./...                              builds the app, then runs everything
//	MVRS_BIN=path/to/app go test ./...         tests an existing binary instead
//	MVRS_RACE=1 go test -race ./...            builds the app with the race detector (needs cgo)

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var appBin string

func TestMain(m *testing.M) {
	appBin = os.Getenv("MVRS_BIN")
	tmp := ""
	if appBin == "" {
		tmp, _ = os.MkdirTemp("", "mvrs-test")
		appBin = filepath.Join(tmp, "multiviewer-replay-sync")
		if runtime.GOOS == "windows" {
			appBin += ".exe"
		}
		args := []string{"build"}
		if os.Getenv("MVRS_RACE") != "" {
			args = append(args, "-race")
		}
		cmd := exec.Command("go", append(args, "-o", appBin, ".")...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Println("building the app failed:", err)
			os.Exit(1)
		}
	} else {
		appBin, _ = filepath.Abs(appBin)
	}
	code := m.Run()
	if tmp != "" {
		os.RemoveAll(tmp)
	}
	os.Exit(code)
}

// ---------------------------------------------------------------- fake MultiViewer

// Two players: "1" = main feed, "2" = an onboard that starts 7 s off and only realigns via playerSync.
type fakePlayer struct {
	base   float64
	anchor time.Time
	paused bool
}

func (p *fakePlayer) pos() float64 {
	if p.paused {
		return p.base
	}
	return p.base + time.Since(p.anchor).Seconds()
}
func (p *fakePlayer) setPos(t float64) { p.base, p.anchor = t, time.Now() }
func (p *fakePlayer) setPaused(v bool) { p.setPos(p.pos()); p.paused = v }

type fakeMV struct {
	mu         sync.Mutex
	players    map[string]*fakePlayer
	syncs      int
	badQueries int // anything not exactly in the expected shape, e.g. an injection attempt
	srv        *httptest.Server
}

// Mutations must match exactly, so anything injected into a query shows up as a bad query.
var (
	reSetPaused = regexp.MustCompile(`^mutation \{ playerSetPaused\(id:"(\d)", paused:(true|false)\) \}$`)
	reSeek      = regexp.MustCompile(`^mutation \{ playerSeekTo\(id:"(\d)", absolute:([\d.e+-]+)\) \}$`)
	reSync      = regexp.MustCompile(`^mutation \{ playerSync\(id:"(\d)"\) \}$`)
)

func newFakeMV(t *testing.T, start float64) *fakeMV {
	now := time.Now()
	f := &fakeMV{players: map[string]*fakePlayer{"1": {base: start, anchor: now}, "2": {base: start + 7, anchor: now}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMV) env() string { return "MULTIVIEWER_URL=" + f.srv.URL + "/api/graphql" }

func (f *fakeMV) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	q := req.Query
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(data any) { json.NewEncoder(w).Encode(map[string]any{"data": data}) }

	if strings.HasPrefix(q, "{ players") {
		ids := []string{"1", "2"}
		sort.Strings(ids)
		list := []any{}
		for _, id := range ids {
			p := f.players[id]
			var driver any
			title := "F1 LIVE"
			if id == "2" {
				driver, title = map[string]any{"tla": "VER"}, "VER"
			}
			list = append(list, map[string]any{
				"id":         id,
				"state":      map[string]any{"paused": p.paused, "live": false, "interpolatedCurrentTime": p.pos()},
				"driverData": driver,
				"streamData": map[string]any{"contentId": id, "sessionKey": "11253", "title": title},
			})
		}
		reply(map[string]any{"players": list})
		return
	}
	if m := reSetPaused.FindStringSubmatch(q); m != nil {
		f.players[m[1]].setPaused(m[2] == "true")
		reply(map[string]any{"playerSetPaused": f.players[m[1]].paused})
		return
	}
	if m := reSeek.FindStringSubmatch(q); m != nil {
		var t float64
		fmt.Sscan(m[2], &t)
		f.players[m[1]].setPos(t)
		reply(map[string]any{"playerSeekTo": f.players[m[1]].pos()})
		return
	}
	if m := reSync.FindStringSubmatch(q); m != nil { // MultiViewer's sync: other players follow the given one
		src := f.players[m[1]].pos()
		for id, p := range f.players {
			if id != m[1] {
				p.setPos(src)
			}
		}
		f.syncs++
		reply(map[string]any{"playerSync": true})
		return
	}
	f.badQueries++
	json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "unsupported query: " + q}}})
}

// The local user, acting on the main feed.
func (f *fakeMV) userPause()         { f.mu.Lock(); f.players["1"].setPaused(true); f.mu.Unlock() }
func (f *fakeMV) userResume()        { f.mu.Lock(); f.players["1"].setPaused(false); f.mu.Unlock() }
func (f *fakeMV) userSeek(t float64) { f.mu.Lock(); f.players["1"].setPos(t); f.mu.Unlock() }

type mvState struct {
	Paused        bool
	Time          float64
	OnboardPaused bool
	OnboardTime   float64
	Syncs         int
	BadQueries    int
}

func (f *fakeMV) state() mvState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return mvState{f.players["1"].paused, f.players["1"].pos(), f.players["2"].paused, f.players["2"].pos(), f.syncs, f.badQueries}
}

// ---------------------------------------------------------------- helpers

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type proc struct {
	out      *syncBuffer
	done     chan struct{}
	exitCode int
}

// startApp runs the app with the given environment entries and arguments; it is killed when the test ends.
func startApp(t *testing.T, env []string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(appBin, args...)
	cmd.Env = append(os.Environ(), env...)
	p := &proc{out: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the app: %v", err)
	}
	go func() {
		cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-p.done
		if t.Failed() {
			t.Logf("--- %v ---\n%s", args, p.out.String())
		}
	})
	return p
}

// exited waits up to d for the process to end and returns its exit code (-1 if still running).
func (p *proc) exited(d time.Duration) int {
	select {
	case <-p.done:
		return p.exitCode
	case <-time.After(d):
		return -1
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return cond()
}

func states(mvs ...*fakeMV) []mvState {
	out := make([]mvState, len(mvs))
	for i, f := range mvs {
		out[i] = f.state()
	}
	return out
}

func together(ss []mvState, tol float64) bool {
	for _, s := range ss {
		if math.Abs(s.Time-ss[0].Time) >= tol {
			return false
		}
	}
	return true
}

func all(ss []mvState, cond func(mvState) bool) bool {
	for _, s := range ss {
		if !cond(s) {
			return false
		}
	}
	return true
}

func check(t *testing.T, name string, ok bool, detail any) {
	t.Helper()
	if !ok {
		t.Errorf("%s: %+v", name, detail)
	}
}

// ---------------------------------------------------------------- sync between three people

func TestSync(t *testing.T) {
	h, a, b := newFakeMV(t, 600), newFakeMV(t, 100), newFakeMV(t, 900) // different starting positions
	startApp(t, []string{h.env()}, "host", "--port", "18791", "--name", "host")
	time.Sleep(1500 * time.Millisecond)
	startApp(t, []string{a.env()}, "join", "127.0.0.1:18791", "--name", "anna")
	ben := startApp(t, []string{b.env()}, "join", "127.0.0.1:18791", "--name", "ben")
	ss := func() []mvState { return states(h, a, b) }
	between := func(lo, hi float64) func(mvState) bool {
		return func(s mvState) bool { return s.Time > lo && s.Time < hi }
	}

	waitFor(6*time.Second, func() bool { return together(ss(), 1.5) })
	check(t, "joiners snap to the host's position", together(ss(), 1.5), ss())

	a.userPause()
	waitFor(6*time.Second, func() bool { s := ss(); return all(s, func(x mvState) bool { return x.Paused }) && together(s, 0.5) })
	check(t, "a joiner's pause pauses everyone", all(ss(), func(x mvState) bool { return x.Paused }), ss())
	check(t, "everyone paused on the same frame", together(ss(), 0.5), ss())

	b.userSeek(300)
	waitFor(6*time.Second, func() bool { return all(ss(), between(299.4, 300.6)) })
	check(t, "seek while paused moves everyone", all(ss(), between(299.4, 300.6)), ss())

	h.userResume()
	waitFor(6*time.Second, func() bool { s := ss(); return all(s, func(x mvState) bool { return !x.Paused }) && together(s, 1.5) })
	check(t, "the host's resume resumes everyone", all(ss(), func(x mvState) bool { return !x.Paused }), ss())
	check(t, "playing in sync", together(ss(), 1.5), ss())

	a.userSeek(500)
	waitFor(6*time.Second, func() bool { return all(ss(), between(499, 506)) })
	check(t, "seek while playing moves everyone", all(ss(), func(x mvState) bool { return between(499, 506)(x) && !x.Paused }), ss())

	onboardAligned := func(x mvState) bool { return math.Abs(x.OnboardTime-x.Time) < 1.5 }
	waitFor(6*time.Second, func() bool { return all(ss(), onboardAligned) })
	check(t, "onboards line up with the main feed", all(ss(), onboardAligned), ss())
	check(t, "MultiViewer's sync was called on every client", all(ss(), func(x mvState) bool { return x.Syncs > 0 }), ss())

	b.userPause()
	waitFor(6*time.Second, func() bool { return all(ss(), func(x mvState) bool { return x.OnboardPaused }) })
	check(t, "a pause reaches everyone's onboards", all(ss(), func(x mvState) bool { return x.OnboardPaused }), ss())
	b.userResume()
	waitFor(6*time.Second, func() bool { return all(ss(), func(x mvState) bool { return !x.OnboardPaused && onboardAligned(x) }) })
	check(t, "a resume reaches the onboards and they realign", all(ss(), func(x mvState) bool { return !x.OnboardPaused }), ss())

	// quick repeated actions must all count, not be undone by the room's echo
	a.userSeek(1000)
	time.Sleep(600 * time.Millisecond)
	a.userSeek(1100)
	time.Sleep(600 * time.Millisecond)
	a.userSeek(1200)
	waitFor(5*time.Second, func() bool { return all(ss(), between(1199, 1206)) })
	time.Sleep(2 * time.Second)
	check(t, "rapid repeated seeks all count (last one wins)", all(ss(), between(1199, 1210)), ss())

	b.userPause()
	time.Sleep(700 * time.Millisecond)
	b.userResume()
	time.Sleep(3 * time.Second)
	check(t, "a quick pause + resume ends up playing everywhere", all(ss(), func(x mvState) bool { return !x.Paused }) && together(ss(), 1.5), ss())

	resyncs := strings.Count(ben.out.String(), "Resynced")
	time.Sleep(5 * time.Second)
	check(t, "stays in sync over time", together(ss(), 1.5), ss())
	check(t, "no resync loop", strings.Count(ben.out.String(), "Resynced") == resyncs, ben.out.String())
}

// ---------------------------------------------------------------- room security

type rawClient struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	received []map[string]any
	closed   chan int // close code
}

func dialRaw(t *testing.T, url string) *rawClient {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	c := &rawClient{conn: conn, closed: make(chan int, 1)}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				code := -1
				if ce, ok := err.(*websocket.CloseError); ok {
					code = ce.Code
				}
				c.closed <- code
				return
			}
			var m map[string]any
			json.Unmarshal(data, &m)
			c.mu.Lock()
			c.received = append(c.received, m)
			c.mu.Unlock()
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return c
}

func (c *rawClient) send(v any) {
	b, ok := v.([]byte)
	if !ok {
		b, _ = json.Marshal(v)
	}
	c.conn.WriteMessage(websocket.TextMessage, b)
}

func TestRoomSecurity(t *testing.T) {
	h, a := newFakeMV(t, 600), newFakeMV(t, 100)
	startApp(t, []string{h.env()}, "host", "--port", "18792", "--name", "host")
	time.Sleep(1500 * time.Millisecond)
	anna := startApp(t, []string{a.env()}, "join", "127.0.0.1:18792", "--name", "anna")
	waitFor(6*time.Second, func() bool { return strings.Contains(anna.out.String(), "Joined the room") })

	spy := dialRaw(t, "ws://127.0.0.1:18792")
	spy.send(map[string]any{"type": "set", "paused": true, "time": 50, "sessionKey": "99999", "host": true, "seq": 1, "by": "spoof"})
	spy.send(map[string]any{"type": "set", "paused": true, "time": "1) } mutation { x", "seq": 2, "by": "inject"})
	time.Sleep(time.Second)
	spy.mu.Lock()
	var switched, injected bool
	for _, m := range spy.received {
		switched = switched || m["sessionKey"] == "99999"
		injected = injected || m["by"] == "inject"
	}
	got := fmt.Sprint(spy.received)
	spy.mu.Unlock()
	check(t, "non-host can't switch the room's session", !switched, got)
	check(t, "invalid messages are dropped by the server", !injected, got)

	spy.send(bytes.Repeat([]byte("x"), 10_000))
	code := -1
	select {
	case code = <-spy.closed:
	case <-time.After(2 * time.Second):
	}
	check(t, "oversized messages close the connection", code == websocket.CloseMessageTooBig, code)

	check(t, "nothing injected reached MultiViewer", h.state().BadQueries == 0 && a.state().BadQueries == 0, states(h, a))
	check(t, "joiners never logged an error", !regexp.MustCompile(`Error|not reachable`).MatchString(anna.out.String()), anna.out.String())

	// a web page in someone's browser must not be able to join
	_, res, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:18792", http.Header{"Origin": {"http://evil.example"}})
	check(t, "browser connections are refused", err != nil && res != nil && res.StatusCode == http.StatusForbidden, err)
}

func TestMaliciousHost(t *testing.T) {
	upgrader := websocket.Upgrader{}
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(v map[string]any) { b, _ := json.Marshal(v); conn.WriteMessage(websocket.TextMessage, b) }
		send(map[string]any{"type": "hello", "id": 1})
		send(map[string]any{"type": "state", "paused": false, "time": `1) } mutation { playerDelete(id:"1"`, "rev": 1, "from": 2, "seq": 1, "by": "x"})
		send(map[string]any{"type": "state", "paused": true, "time": 42, "sessionKey": `1") }`, "rev": 2, "from": 2, "seq": 1, "by": "x"})
		send(map[string]any{"type": "state", "paused": false, "time": 70, "sessionKey": "11253", "rev": 3, "from": 2, "seq": 1, "by": "x"})
		time.Sleep(1500 * time.Millisecond) // a second, valid change so the (hostile) name gets printed
		send(map[string]any{"type": "state", "paused": true, "time": 75, "sessionKey": "11253", "rev": 4, "from": 2, "seq": 2, "by": "\x1b[2J\x1b[31mevil"})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer evil.Close()

	e := newFakeMV(t, 10)
	victim := startApp(t, []string{e.env()}, "join", strings.TrimPrefix(evil.URL, "http://"), "--name", "victim")
	followed := func() bool { s := e.state(); return s.Paused && math.Abs(s.Time-75) < 0.5 }
	waitFor(6*time.Second, followed)
	check(t, "a joiner rejects malformed state but follows valid state", e.state().BadQueries == 0 && followed(), e.state())
	out := victim.out.String()
	check(t, "control characters in names are stripped", strings.Contains(out, "evil") && !strings.Contains(out, "\x1b"), out)
}

func TestPassword(t *testing.T) {
	p1, p2, p3 := newFakeMV(t, 200), newFakeMV(t, 400), newFakeMV(t, 800)
	host := startApp(t, []string{p1.env()}, "host", "--port", "18793", "--name", "host2", "--password", "s3cret")
	time.Sleep(1500 * time.Millisecond)
	wrong := startApp(t, []string{p2.env()}, "join", "127.0.0.1:18793", "--name", "intruder", "--password", "nope")
	none := startApp(t, []string{p2.env()}, "join", "127.0.0.1:18793", "--name", "nopass")
	right := startApp(t, []string{p3.env()}, "join", "127.0.0.1:18793", "--name", "friend", "--password", "s3cret")

	check(t, "wrong password is rejected", wrong.exited(5*time.Second) == 1 && strings.Contains(wrong.out.String(), "Wrong room password"), wrong.out.String())
	check(t, "missing password is rejected", none.exited(5*time.Second) == 1 && strings.Contains(none.out.String(), "Wrong room password"), none.out.String())
	waitFor(6*time.Second, func() bool { return together(states(p1, p3), 1.5) })
	check(t, "right password joins and syncs", together(states(p1, p3), 1.5) && strings.Contains(right.out.String(), "Joined the room"), right.out.String())
	check(t, "the host's own client gets in too", !strings.Contains(host.out.String(), "Wrong room password"), host.out.String())
}

func TestDeadConnection(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{}
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil) // accepts, then never says anything
		if err != nil {
			return
		}
		connections.Add(1)
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer silent.Close()

	d := newFakeMV(t, 10)
	startApp(t, []string{d.env(), "MVRS_HEARTBEAT_MS=300"}, "join", strings.TrimPrefix(silent.URL, "http://"), "--name", "lonely")
	waitFor(8*time.Second, func() bool { return connections.Load() >= 2 })
	check(t, "a silent connection is detected and re-established", connections.Load() >= 2, connections.Load())
}
