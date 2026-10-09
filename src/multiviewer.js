// Thin client for MultiViewer's local GraphQL API.

const API_URL = process.env.MULTIVIEWER_URL ?? "http://localhost:10101/api/graphql";
const TIMEOUT_MS = 3000;

// Values are written into queries through these encoders only, so nothing coming
// from the network can change the shape of a query.
const str = (v) => JSON.stringify(String(v));
const bool = (v) => (v ? "true" : "false");
const num = (v) => {
  if (typeof v !== "number" || !Number.isFinite(v)) throw new Error(`invalid number: ${v}`);
  return String(v);
};

async function gql(query) {
  const res = await fetch(API_URL, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ query }),
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
  const json = await res.json();
  if (json.errors) throw new Error(json.errors.map((e) => e.message).join("; "));
  return json.data;
}

/** All open players. `onboard` is true for driver onboard windows. */
export async function getPlayers() {
  const data = await gql(
    `{ players { id state { paused live interpolatedCurrentTime } driverData { tla } streamData { contentId sessionKey title } } }`
  );
  return data.players.map((p) => ({
    id: p.id,
    onboard: Boolean(p.driverData),
    label: p.driverData?.tla ?? p.streamData?.title ?? "player",
    paused: p.state.paused,
    live: p.state.live,
    time: p.state.interpolatedCurrentTime,
    sessionKey: p.streamData?.sessionKey ?? null,
  }));
}

export async function setPaused(id, paused) {
  await gql(`mutation { playerSetPaused(id:${str(id)}, paused:${bool(paused)}) }`);
}

export async function seekTo(id, absolute) {
  await gql(`mutation { playerSeekTo(id:${str(id)}, absolute:${num(absolute)}) }`);
}

/** MultiViewer's own "sync": aligns the other players (onboards) and the live timing to this player. */
export async function syncOthersTo(id) {
  await gql(`mutation { playerSync(id:${str(id)}) }`);
}
