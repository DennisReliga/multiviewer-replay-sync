// End-to-end tests against fake MultiViewer players. Run: npm test
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { WebSocket, WebSocketServer } from "ws";

const dir = path.dirname(fileURLToPath(import.meta.url));
const cli = path.join(dir, "..", "src", "cli.js");
const procs = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function run(args, env = {}, command = process.execPath) {
  const p = spawn(command, args, { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
  p.output = "";
  p.stdout.on("data", (d) => (p.output += d));
  p.stderr.on("data", (d) => (p.output += d));
  p.exited = new Promise((r) => p.on("exit", r));
  procs.push(p);
  return p;
}
const mock = (port, start) => run([path.join(dir, "mock-multiviewer.js"), port, start]);
// MVRS_BIN=dist/multiviewer-replay-sync.exe npm test  runs the same tests against a built executable
const app = (args, env) => (process.env.MVRS_BIN ? run(args, env, path.resolve(process.env.MVRS_BIN)) : run([cli, ...args], env));
const mv = (port) => ({ MULTIVIEWER_URL: `http://localhost:${port}/api/graphql` });
const user = async (port, action, q = "") => (await fetch(`http://localhost:${port}/user/${action}${q}`)).json();
const states = (ports) => Promise.all(ports.map((p) => user(p, "state")));

/** Poll until `fn` returns true (or time runs out); returns the last value of `get`. */
async function waitFor(get, fn, ms = 6000) {
  const end = Date.now() + ms;
  let v;
  while (Date.now() < end) {
    v = await get();
    if (fn(v)) return v;
    await sleep(200);
  }
  return v;
}

let failed = 0;
function check(name, ok, detail = "") {
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${ok ? "" : "\n      " + (typeof detail === "string" ? detail : JSON.stringify(detail))}`);
  if (!ok) failed++;
}

function rawClient(url) {
  const ws = new WebSocket(url);
  ws.received = [];
  ws.on("message", (d) => ws.received.push(JSON.parse(d.toString())));
  ws.closed = new Promise((r) => ws.on("close", (code) => r(code)));
  ws.on("error", () => {});
  return new Promise((r) => ws.on("open", () => r(ws)));
}

try {
  // ---------------------------------------------------------------- sync between three people
  console.log("\n# Sync");
  const H = 11001, A = 11002, B = 11003; // fake MultiViewers with different starting positions
  mock(H, 600), mock(A, 100), mock(B, 900);
  await sleep(500);
  const host = app(["host", "--port", "8791", "--name", "host"], mv(H));
  await sleep(1500);
  const anna = app(["join", "127.0.0.1:8791", "--name", "anna"], mv(A));
  const ben = app(["join", "127.0.0.1:8791", "--name", "ben"], mv(B));
  const all = () => states([H, A, B]);
  const close = (s, d) => Math.abs(s.time - d.time) < d.tol;
  const together = (ss, tol = 1.5) => ss.every((s) => Math.abs(s.time - ss[0].time) < tol);

  let ss = await waitFor(all, (ss) => together(ss));
  check("joiners snap to the host's position", together(ss), ss);

  await user(A, "pause");
  ss = await waitFor(all, (ss) => ss.every((s) => s.paused) && together(ss, 0.5));
  check("a joiner's pause pauses everyone", ss.every((s) => s.paused), ss);
  check("everyone paused on the same frame", together(ss, 0.5), ss);

  await user(B, "seek", "?to=300");
  ss = await waitFor(all, (ss) => ss.every((s) => Math.abs(s.time - 300) < 0.6));
  check("seek while paused moves everyone", ss.every((s) => Math.abs(s.time - 300) < 0.6), ss);

  await user(H, "resume");
  ss = await waitFor(all, (ss) => ss.every((s) => !s.paused) && together(ss));
  check("the host's resume resumes everyone", ss.every((s) => !s.paused), ss);
  check("playing in sync", together(ss), ss);

  await user(A, "seek", "?to=500");
  ss = await waitFor(all, (ss) => ss.every((s) => s.time > 499 && s.time < 506));
  check("seek while playing moves everyone", ss.every((s) => s.time > 499 && s.time < 506 && !s.paused), ss);

  ss = await waitFor(all, (ss) => ss.every((s) => Math.abs(s.onboardTime - s.time) < 1.5));
  check("onboards line up with the main feed", ss.every((s) => Math.abs(s.onboardTime - s.time) < 1.5), ss);
  check("MultiViewer's sync was called on every client", ss.every((s) => s.syncs > 0), ss);

  await user(B, "pause");
  ss = await waitFor(all, (ss) => ss.every((s) => s.onboardPaused));
  check("a pause reaches everyone's onboards", ss.every((s) => s.onboardPaused), ss);
  await user(B, "resume");
  ss = await waitFor(all, (ss) => ss.every((s) => !s.onboardPaused && Math.abs(s.onboardTime - s.time) < 1.5));
  check("a resume reaches the onboards and they realign", ss.every((s) => !s.onboardPaused), ss);

  // quick repeated actions must all count, not be undone by the room's echo
  await user(A, "seek", "?to=1000");
  await sleep(600);
  await user(A, "seek", "?to=1100");
  await sleep(600);
  await user(A, "seek", "?to=1200");
  ss = await waitFor(all, (ss) => ss.every((s) => s.time > 1199 && s.time < 1206), 5000);
  await sleep(2000);
  ss = await all();
  check("rapid repeated seeks all count (last one wins)", ss.every((s) => s.time > 1199 && s.time < 1210), ss);

  await user(B, "pause");
  await sleep(700);
  await user(B, "resume");
  await sleep(3000);
  ss = await all();
  check("a quick pause + resume ends up playing everywhere", ss.every((s) => !s.paused) && together(ss), ss);

  const resyncs = (ben.output.match(/Resynced/g) ?? []).length;
  await sleep(5000);
  ss = await all();
  check("stays in sync over time", together(ss), ss);
  check("no resync loop", (ben.output.match(/Resynced/g) ?? []).length === resyncs, ben.output);

  // ---------------------------------------------------------------- room security
  console.log("\n# Room security");
  const spy = await rawClient("ws://127.0.0.1:8791");
  spy.send(JSON.stringify({ type: "set", paused: true, time: 50, sessionKey: "99999", host: true, seq: 1, by: "spoof" }));
  spy.send(JSON.stringify({ type: "set", paused: true, time: "1) } mutation { x", seq: 2, by: "inject" }));
  await sleep(1000);
  const seen = spy.received.filter((m) => m.type === "state" && m.rev > 0 && m.by !== "host" && m.by !== "anna" && m.by !== "ben");
  check("non-host can't switch the room's session", !spy.received.some((m) => m.sessionKey === "99999"), spy.received);
  check("invalid messages are dropped by the server", seen.length === 0, seen);
  spy.send("x".repeat(10_000));
  check("oversized messages close the connection", (await Promise.race([spy.closed, sleep(2000)])) === 1009);

  ss = await all();
  check("nothing injected reached MultiViewer", ss.every((s) => s.badQueries === 0), ss);
  check("joiners never logged an error", !/Error|not reachable/.test(anna.output + ben.output), anna.output + ben.output);

  // a malicious host sends bogus state to a joiner
  const evil = new WebSocketServer({ port: 8795 });
  evil.on("connection", (ws) => {
    ws.send(JSON.stringify({ type: "hello", id: 1 }));
    ws.send(JSON.stringify({ type: "state", paused: false, time: '1) } mutation { playerDelete(id:"1"', rev: 1, from: 2, seq: 1, by: "x" }));
    ws.send(JSON.stringify({ type: "state", paused: true, time: 42, sessionKey: '1") }', rev: 2, from: 2, seq: 1, by: "x" }));
    ws.send(JSON.stringify({ type: "state", paused: false, time: 70, sessionKey: "11253", rev: 3, from: 2, seq: 1, by: "x" }));
    // a second, valid change so the (hostile) name gets printed
    setTimeout(() => ws.send(JSON.stringify({ type: "state", paused: true, time: 75, sessionKey: "11253", rev: 4, from: 2, seq: 2, by: "\u001b[2J\u001b[31mevil" })), 1500);
  });
  const E = 11004;
  mock(E, 10);
  await sleep(500);
  const victim = app(["join", "127.0.0.1:8795", "--name", "victim"], mv(E));
  const ev = await waitFor(() => user(E, "state"), (s) => s.paused && Math.abs(s.time - 75) < 0.5, 6000);
  check("a joiner rejects malformed state but follows valid state", ev.badQueries === 0 && ev.paused && Math.abs(ev.time - 75) < 0.5, ev);
  check("control characters in names are stripped", victim.output.includes("evil") && !victim.output.includes("\u001b"), victim.output);
  victim.kill();
  evil.close();

  // ---------------------------------------------------------------- password
  console.log("\n# Password");
  const P1 = 11005, P2 = 11006, P3 = 11007;
  mock(P1, 200), mock(P2, 400), mock(P3, 800);
  await sleep(500);
  const locked = app(["host", "--port", "8792", "--name", "host2", "--password", "s3cret"], mv(P1));
  await sleep(1500);
  const wrong = app(["join", "127.0.0.1:8792", "--name", "intruder", "--password", "nope"], mv(P2));
  const none = app(["join", "127.0.0.1:8792", "--name", "nopass"], mv(P2));
  const right = app(["join", "127.0.0.1:8792", "--name", "friend", "--password", "s3cret"], mv(P3));
  const [wrongCode, noneCode] = await Promise.all([
    Promise.race([wrong.exited, sleep(5000)]),
    Promise.race([none.exited, sleep(5000)]),
  ]);
  check("wrong password is rejected", wrongCode === 1 && /Wrong room password/.test(wrong.output), wrong.output);
  check("missing password is rejected", noneCode === 1 && /Wrong room password/.test(none.output), none.output);
  const pr = await waitFor(() => states([P1, P3]), (ss) => together(ss));
  check("right password joins and syncs", together(pr) && /Joined the room/.test(right.output), { pr, out: right.output });
  check("the host's own client gets in too", !/Wrong room password/.test(locked.output), locked.output);

  // ---------------------------------------------------------------- dead connections
  console.log("\n# Dead connections");
  let connections = 0;
  const silent = new WebSocketServer({ port: 8796 }); // accepts, then never says anything
  silent.on("connection", () => connections++);
  const D = 11008;
  mock(D, 10);
  await sleep(500);
  app(["join", "127.0.0.1:8796", "--name", "lonely"], { ...mv(D), MVRS_HEARTBEAT_MS: "300" });
  await waitFor(async () => connections, (n) => n >= 2, 8000);
  check("a silent connection is detected and re-established", connections >= 2, `connections: ${connections}`);
  silent.close();

  if (failed) console.log(`\n--- host ---\n${host.output}\n--- anna ---\n${anna.output}\n--- ben ---\n${ben.output}`);
} catch (e) {
  console.error(e);
  failed++;
} finally {
  procs.forEach((p) => p.kill());
  console.log(failed ? `\n${failed} FAILED` : "\nAll passed");
  process.exit(failed ? 1 : 0);
}
