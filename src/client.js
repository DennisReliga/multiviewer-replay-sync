import { WebSocket } from "ws";
import { getPlayers, setPaused, seekTo, syncOthersTo } from "./multiviewer.js";
import { parseState, HEARTBEAT_MS } from "./protocol.js";

const POLL_MS = 500;
const DRIFT_SECONDS = 1.5; // resync to the room if this far off while playing
const PAUSED_DRIFT_SECONDS = 0.3; // tighter while paused so everyone sits on the same frame
const SEEK_JUMP_SECONDS = 1.0; // a sudden position change bigger than this is the user seeking
const SEEK_COOLDOWN_MS = 3000; // let the stream settle after we seek ourselves
const SYNC_DELAY_MS = 1500; // after a pause/resume/seek, wait for the stream to settle, then align onboards + live timing
const ECHO_TIMEOUT_MS = 3000; // give up waiting for the room to confirm our own change
const RECONNECT_MS = 3000;
const DEAD_AFTER_MS = HEARTBEAT_MS * 2.5; // no ping or message from the server for this long = connection is dead

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const fmt = (s) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;

/**
 * Every client (including the host's) does the same thing each tick:
 *  1. notice if the local user paused/resumed/seeked -> tell the room
 *  2. apply changes other people made
 *  3. otherwise correct slow drift against the room
 */
export async function runClient({ url, name, password, hostToken, playerId, log, onFatal }) {
  const isHost = Boolean(hostToken);
  let ws = null;
  let myId = null; // our connection id, assigned by the server
  let seq = 0; // number of our last "set"
  let ackedSeq = 0; // last of our "set"s the room has confirmed
  let sentAt = 0;
  let room = null; // accepted room state, plus recvAt and own
  let latest = null; // newest state from the server, even if we're ignoring it for now
  let appliedPaused = null; // paused state of the last room state we applied (ours or someone else's)
  let appliedRev = -1;
  let last = null; // previous local sample, for spotting user actions
  let cooldownUntil = 0;
  let syncAt = 0; // when to next call MultiViewer's sync (0 = none due)
  let lastHeard = 0;
  let everConnected = false;
  let warnedUnreachable = false;
  const warned = { player: null, session: null, mv: false };

  const waitingForEcho = () => seq > ackedSeq;
  const expected = (r, now) => (r.paused ? r.time : r.time + (now - r.recvAt) / 1000);
  const sample = (p, now) => ({ paused: p.paused, time: p.time, t: now });

  function sendSet(p, now) {
    if (ws?.readyState !== WebSocket.OPEN) return false;
    seq++;
    sentAt = now;
    ws.send(JSON.stringify({ type: "set", paused: p.paused, time: p.time, sessionKey: p.sessionKey, seq, by: name }));
    appliedRev = room?.rev ?? -1; // whatever the room said before is superseded by this
    appliedPaused = p.paused;
    return true;
  }

  function accept(state) {
    room = { ...state, recvAt: Date.now(), own: state.from === myId };
  }

  function connect() {
    const headers = {};
    if (password) headers["x-room-password"] = password;
    if (hostToken) headers["x-host-token"] = hostToken;
    ws = new WebSocket(url, { headers, maxPayload: 64 * 1024 });

    ws.on("open", () => {
      everConnected = true;
      warnedUnreachable = false;
      lastHeard = Date.now();
      seq = ackedSeq = 0;
      appliedRev = -1;
      appliedPaused = null;
      if (!isHost) log(`Connected to ${url}`);
    });
    ws.on("ping", () => (lastHeard = Date.now()));
    ws.on("message", (data) => {
      lastHeard = Date.now();
      let m;
      try {
        m = JSON.parse(data.toString());
      } catch {
        return;
      }
      if (m?.type === "hello" && Number.isSafeInteger(m.id)) {
        myId = m.id;
        return;
      }
      const state = parseState(m);
      if (!state) return;
      latest = state;
      if (state.from === myId) {
        // echo of our own change; an older echo is already superseded by a newer one in flight
        if (state.seq >= seq) {
          ackedSeq = state.seq;
          accept(state);
        }
      } else if (!waitingForEcho()) {
        // while our own change is in flight, anything earlier from others is superseded by it
        accept(state);
      }
    });
    ws.on("error", (e) => {
      if (/\b401\b/.test(e.message)) onFatal("Wrong room password.");
    });
    ws.on("close", () => {
      if (everConnected) log(`Lost connection to ${isHost ? "the room" : "host"}, reconnecting...`);
      else if (!warnedUnreachable) log(`Can't reach ${url} yet - is the host running? Retrying every ${RECONNECT_MS / 1000}s...`);
      everConnected = false;
      warnedUnreachable = true;
      myId = null;
      setTimeout(connect, RECONNECT_MS);
    });
  }
  connect();

  function announce() {
    if (appliedPaused === null) log(`Joined the room (${room.paused ? "paused" : "playing"} at ${fmt(room.time)})`);
    else if (appliedPaused !== room.paused) log(`${room.by} ${room.paused ? "paused" : "resumed"} at ${fmt(room.time)}`);
    else log(`${room.by} jumped to ${fmt(room.time)}`);
  }

  function pickPlayer(players) {
    if (playerId) return players.find((x) => x.id === String(playerId));
    return players.find((x) => !x.onboard) ?? players[0];
  }

  /**
   * Make the local player match the room. `remoteChange` = someone just paused/seeked on purpose:
   * apply it right away. Otherwise this is drift correction, which backs off after a seek.
   */
  async function reconcile(p, all, now, remoteChange = false) {
    // A newer state can arrive while we await MultiViewer below; work on the one we started with
    // (the newer one is applied next tick).
    const target = room;
    let acted = false;
    let time = p.time;
    if (p.paused !== target.paused) {
      // pause/resume every open window (main feed and onboards), not just the followed one
      for (const x of all) if (x.paused !== target.paused) await setPaused(x.id, target.paused);
      acted = true;
    }
    const exp = expected(target, now);
    const drift = p.time - exp;
    if (Math.abs(drift) > (target.paused ? PAUSED_DRIFT_SECONDS : DRIFT_SECONDS) && (remoteChange || now >= cooldownUntil)) {
      await seekTo(p.id, exp);
      time = exp;
      cooldownUntil = now + SEEK_COOLDOWN_MS;
      log(`Resynced to ${fmt(exp)} (was ${drift > 0 ? "+" : ""}${drift.toFixed(1)}s off)`);
      acted = true;
    }
    if (acted) syncAt = now + SYNC_DELAY_MS;
    // Remember what the player looks like now (after our own changes), so our changes aren't
    // mistaken for the user's, while a user action right after them is still noticed.
    last = { paused: target.paused, time, t: now };
  }

  async function tick() {
    const all = await getPlayers();
    const now = Date.now();
    warned.mv = false;

    // A connection that went quiet is dead even if the OS hasn't noticed yet.
    if (ws?.readyState === WebSocket.OPEN && now - lastHeard > DEAD_AFTER_MS) ws.terminate();

    const p = pickPlayer(all);
    if (!p) {
      const msg = !all.length
        ? "No MultiViewer player open - open the replay."
        : `Player ${playerId} not found. Open players: ${all.map((x) => `${x.id} (${x.label})`).join(", ")}`;
      if (warned.player !== msg) log(msg);
      warned.player = msg;
      last = null;
      return;
    }
    warned.player = null;

    if (p.live && !warned.live) {
      log("This is a live stream. Only replays can be synced (live streams can't be seeked).");
      warned.live = true;
    }

    // The room didn't confirm our change in time (dropped, or connection trouble): follow whatever it says now.
    if (waitingForEcho() && now - sentAt > ECHO_TIMEOUT_MS) {
      ackedSeq = seq;
      if (latest) accept(latest);
    }

    // First client in the room (the host) seeds it.
    if (!room) {
      if (isHost && !waitingForEcho()) sendSet(p, now);
      last = sample(p, now);
      return;
    }

    if (room.sessionKey && p.sessionKey && room.sessionKey !== p.sessionKey) {
      if (isHost) {
        if (!waitingForEcho()) sendSet(p, now); // the host switched replays: take the room along
      } else if (warned.session !== p.sessionKey) {
        log(`You have a different session open (${p.sessionKey}) than the room (${room.sessionKey}). Not syncing.`);
        warned.session = p.sessionKey;
      }
      last = null;
      return;
    }
    warned.session = null;

    // Time to line up the onboards and live timing with the main feed?
    if (syncAt && now >= syncAt) {
      syncAt = 0;
      await syncOthersTo(p.id);
    }

    // 1. Did the local user do something? (Checked every tick, so quick repeated seeks all count.)
    if (last) {
      const wall = (now - last.t) / 1000;
      const delta = p.time - last.time;
      const pausedFlip = p.paused !== last.paused;
      const jumped =
        p.paused === last.paused && (p.paused ? Math.abs(delta) > 0.5 : Math.abs(delta - wall) > SEEK_JUMP_SECONDS);
      if ((pausedFlip || jumped) && sendSet(p, now)) {
        log(pausedFlip ? `You ${p.paused ? "paused" : "resumed"}` : `You jumped to ${fmt(p.time)}`);
        // carry a pause/resume over to your own onboard windows too
        if (pausedFlip) for (const x of all) if (x.paused !== p.paused) await setPaused(x.id, p.paused);
        syncAt = now + SYNC_DELAY_MS; // keep your own onboards/timing lined up with what you just did
        last = sample(p, now);
        return;
      }
    }

    // 2. Someone else changed something.
    if (room.rev !== appliedRev) {
      appliedRev = room.rev;
      if (!room.own) announce();
      appliedPaused = room.paused;
      if (!room.own) {
        await reconcile(p, all, now, true);
        return;
      }
    }

    // 3. Slow drift. Not while our own change is still on its way to the room.
    if (waitingForEcho()) {
      last = sample(p, now);
      return;
    }
    await reconcile(p, all, now);
  }

  for (;;) {
    await sleep(POLL_MS);
    try {
      await tick();
    } catch (e) {
      if (!warned.mv) log(`MultiViewer not reachable: ${e.name === "TimeoutError" ? "no response" : e.message}`);
      warned.mv = true;
      last = null;
    }
  }
}
