import crypto from "node:crypto";
import { WebSocketServer, WebSocket } from "ws";
import { parseSet, MAX_MESSAGE_BYTES, HEARTBEAT_MS } from "./protocol.js";

const sha256 = (s) => crypto.createHash("sha256").update(String(s)).digest();

/**
 * Room relay. Holds the single shared playback state; any participant may change it.
 *
 *   server -> client  { type: "hello", id }
 *   client -> server  { type: "set", paused, time, sessionKey, seq, by }
 *   server -> client  { type: "state", paused, time, sessionKey, rev, from, seq, by }
 *
 * `from`/`seq` let a client recognise the echo of its own change.
 * Only the host's own client (which knows `hostToken`) may move the room to another session.
 */
export function startServer({ port, password, log, onFatal }) {
  const hostToken = crypto.randomBytes(16).toString("hex");
  const passwordHash = password ? sha256(password) : null;
  let nextId = 1;
  let room = null; // { paused, time, at, sessionKey, rev, from, seq, by }

  const wss = new WebSocketServer({
    port, // all interfaces, IPv4 and IPv6
    maxPayload: MAX_MESSAGE_BYTES,
    verifyClient: (info, done) => {
      if (!passwordHash) return done(true);
      const given = info.req.headers["x-room-password"];
      const ok = typeof given === "string" && crypto.timingSafeEqual(sha256(given), passwordHash);
      done(ok, 401, "Wrong room password");
    },
  });

  const snapshot = () => ({
    type: "state",
    paused: room.paused,
    // while playing, the stored time is an anchor at `at`; hand out the current position
    time: room.paused ? room.time : room.time + (Date.now() - room.at) / 1000,
    sessionKey: room.sessionKey,
    rev: room.rev,
    from: room.from,
    seq: room.seq,
    by: room.by,
  });

  const broadcast = () => {
    const msg = JSON.stringify(snapshot());
    for (const c of wss.clients) if (c.readyState === WebSocket.OPEN) c.send(msg);
  };

  wss.on("listening", () => log(`Room open on port ${port}${password ? " (password protected)" : ""}.`));
  wss.on("error", (e) => onFatal(e.code === "EADDRINUSE" ? `Port ${port} is already in use.` : e.message));

  wss.on("connection", (ws, req) => {
    ws.id = nextId++;
    ws.isHost = req.headers["x-host-token"] === hostToken;
    ws.alive = true;
    ws.on("pong", () => (ws.alive = true));
    ws.on("error", () => {});
    if (!ws.isHost) log(`Someone connected (${req.socket.remoteAddress}). ${wss.clients.size} in room.`);
    ws.on("close", () => !ws.isHost && log(`Someone left. ${wss.clients.size} in room.`));

    ws.send(JSON.stringify({ type: "hello", id: ws.id }));
    if (room) ws.send(JSON.stringify(snapshot()));

    ws.on("message", (data) => {
      let m;
      try {
        m = parseSet(JSON.parse(data.toString()));
      } catch {}
      if (!m) return;
      if (room?.sessionKey && m.sessionKey && room.sessionKey !== m.sessionKey && !ws.isHost) return;
      room = {
        paused: m.paused,
        time: m.time,
        at: Date.now(),
        sessionKey: m.sessionKey ?? room?.sessionKey ?? null,
        rev: (room?.rev ?? 0) + 1,
        from: ws.id,
        seq: m.seq,
        by: m.by,
      };
      broadcast();
    });
  });

  // Drop connections that silently died (e.g. a ZeroTier peer vanished).
  const heartbeat = setInterval(() => {
    for (const c of wss.clients) {
      if (!c.alive) c.terminate();
      else {
        c.alive = false;
        c.ping();
      }
    }
  }, HEARTBEAT_MS);
  wss.on("close", () => clearInterval(heartbeat));

  return { wss, hostToken };
}
