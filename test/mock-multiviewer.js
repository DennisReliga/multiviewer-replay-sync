// Fake MultiViewer GraphQL API for testing without the real app.
// Two players: "1" = main feed, "2" = an onboard that starts 7 s off and only realigns via playerSync.
// usage: node mock-multiviewer.js <port> [startTime]
// extra endpoints simulate the local user on the main feed:
//   /user/pause, /user/resume, /user/seek?to=N, /user/state
import http from "node:http";

const port = Number(process.argv[2]);
const start = Number(process.argv[3] ?? 600);

const mk = (t) => ({ base: t, anchor: Date.now(), paused: false });
const players = { 1: mk(start), 2: mk(start + 7) };
let syncs = 0;
let badQueries = 0; // anything not exactly in the expected shape, e.g. an injection attempt

const pos = (p) => (p.paused ? p.base : p.base + (Date.now() - p.anchor) / 1000);
const setPos = (p, t) => ((p.base = t), (p.anchor = Date.now()));
const setPaused = (p, v) => (setPos(p, pos(p)), (p.paused = v));

// Mutations must match exactly, so anything injected into a query shows up as a bad query.
const SET_PAUSED = /^mutation \{ playerSetPaused\(id:"(\d)", paused:(true|false)\) \}$/;
const SEEK = /^mutation \{ playerSeekTo\(id:"(\d)", absolute:([\d.e+-]+)\) \}$/;
const SYNC = /^mutation \{ playerSync\(id:"(\d)"\) \}$/;

function gql(query) {
  if (query.startsWith("{ players")) {
    return {
      players: Object.entries(players).map(([id, p]) => ({
        id,
        state: { paused: p.paused, live: false, interpolatedCurrentTime: pos(p) },
        driverData: id === "2" ? { tla: "VER" } : null,
        streamData: { contentId: id, sessionKey: "11253", title: id === "2" ? "VER" : "F1 LIVE" },
      })),
    };
  }
  let m;
  if ((m = query.match(SET_PAUSED))) {
    setPaused(players[m[1]], m[2] === "true");
    return { playerSetPaused: players[m[1]].paused };
  }
  if ((m = query.match(SEEK))) {
    setPos(players[m[1]], Number(m[2]));
    return { playerSeekTo: pos(players[m[1]]) };
  }
  if ((m = query.match(SYNC))) {
    // MultiViewer's sync: other players follow the given one
    const src = players[m[1]];
    for (const [id, p] of Object.entries(players)) if (id !== m[1]) setPos(p, pos(src));
    syncs++;
    return { playerSync: true };
  }
  badQueries++;
  throw new Error("unsupported query: " + query);
}

http
  .createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    res.setHeader("content-type", "application/json");
    if (url.pathname === "/user/pause") setPaused(players[1], true);
    else if (url.pathname === "/user/resume") setPaused(players[1], false);
    else if (url.pathname === "/user/seek") setPos(players[1], Number(url.searchParams.get("to")));
    if (url.pathname.startsWith("/user/")) {
      return res.end(
        JSON.stringify({
          paused: players[1].paused,
          time: pos(players[1]),
          onboardPaused: players[2].paused,
          onboardTime: pos(players[2]),
          syncs,
          badQueries,
        })
      );
    }
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      try {
        res.end(JSON.stringify({ data: gql(JSON.parse(body).query) }));
      } catch (e) {
        res.end(JSON.stringify({ errors: [{ message: e.message }] }));
      }
    });
  })
  .listen(port);
