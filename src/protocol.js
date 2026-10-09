// Shared limits and validation for messages between clients and the room server.

export const DEFAULT_PORT = 8765;
export const MAX_MESSAGE_BYTES = 4096;
export const HEARTBEAT_MS = Number(process.env.MVRS_HEARTBEAT_MS) || 10_000;
const MAX_TIME_SECONDS = 7 * 24 * 3600; // nothing plausible is longer than this

/** Display names come from other people's machines: strip control characters (no terminal escape tricks). */
export function cleanName(s) {
  return (
    String(s ?? "")
      .replace(/[\u0000-\u001f\u007f-\u009f]/g, "")
      .trim()
      .slice(0, 40) || "someone"
  );
}

/** "1.2.3.4", "1.2.3.4:99", "fd00::1", "[fd00::1]:99", "host.name" -> ws:// URL */
export function roomUrl(target) {
  const t = target.trim();
  if (t.startsWith("[")) return `ws://${/\]:\d+$/.test(t) ? t : `${t}:${DEFAULT_PORT}`}`;
  const colons = (t.match(/:/g) ?? []).length;
  if (colons > 1) return `ws://[${t}]:${DEFAULT_PORT}`; // bare IPv6
  return `ws://${colons ? t : `${t}:${DEFAULT_PORT}`}`;
}

export const isValidTime = (t) => typeof t === "number" && Number.isFinite(t) && t >= 0 && t <= MAX_TIME_SECONDS;
const isValidSession = (s) => s === null || s === undefined || (typeof s === "string" && /^[\w-]{1,64}$/.test(s));

/** Validates a "set" sent by a client. Returns the clean message or null. */
export function parseSet(m) {
  if (!m || m.type !== "set") return null;
  if (typeof m.paused !== "boolean" || !isValidTime(m.time) || !isValidSession(m.sessionKey)) return null;
  if (!Number.isSafeInteger(m.seq) || m.seq < 1) return null;
  return { paused: m.paused, time: m.time, sessionKey: m.sessionKey ?? null, seq: m.seq, by: cleanName(m.by) };
}

/** Validates a "state" sent by the server. Returns the clean message or null. */
export function parseState(m) {
  if (!m || m.type !== "state") return null;
  if (typeof m.paused !== "boolean" || !isValidTime(m.time) || !isValidSession(m.sessionKey)) return null;
  if (!Number.isSafeInteger(m.rev) || !Number.isSafeInteger(m.from) || !Number.isSafeInteger(m.seq)) return null;
  return {
    paused: m.paused,
    time: m.time,
    sessionKey: m.sessionKey ?? null,
    rev: m.rev,
    from: m.from,
    seq: m.seq,
    by: cleanName(m.by),
  };
}
