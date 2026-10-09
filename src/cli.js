#!/usr/bin/env node
import os from "node:os";
import fs from "node:fs";
import path from "node:path";
import readline from "node:readline/promises";
import { parseArgs } from "node:util";
import { startServer } from "./server.js";
import { runClient } from "./client.js";
import { DEFAULT_PORT, cleanName, roomUrl } from "./protocol.js";

const APP = "multiviewer-replay-sync";
const CONFIG_DIR = process.env.APPDATA ?? process.env.XDG_CONFIG_HOME ?? path.join(os.homedir(), ".config");
const CONFIG_FILE = path.join(CONFIG_DIR, APP, "config.json");

const cmd = (args, text) => `  ${`${APP} ${args}`.padEnd(48)}${text}`;
const USAGE = `MultiViewer Replay Sync - watch F1 replays in sync with friends

${cmd("", "no arguments: asks what to do")}
${cmd(`host [--port ${DEFAULT_PORT}]`, "open a room and share your IP")}
${cmd("join <ip>[:port]", "join someone's room (IPv6: [addr]:port)")}

Options:
  --name <name>        name shown to others (default: your user name)
  --password <text>    host: require this password; join: the room's password
  --player <id>        which MultiViewer player to follow (default: the main feed)
  -v, --version        show the version
  -h, --help           show this help

Everyone in the room can pause, resume and seek; it is mirrored to all.`;

const log = (msg) => console.log(`[${new Date().toLocaleTimeString()}] ${msg}`);

/** Print, wait for Enter when started by double-click (so the window doesn't vanish), exit. */
async function fatal(msg) {
  console.error(`\nError: ${msg}`);
  if (process.stdin.isTTY) {
    const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
    await rl.question("Press Enter to exit...");
    rl.close();
  }
  process.exit(1);
}

function defaultName() {
  try {
    return os.userInfo().username;
  } catch {
    return "someone";
  }
}

function loadConfig() {
  try {
    return JSON.parse(fs.readFileSync(CONFIG_FILE, "utf8"));
  } catch {
    return {};
  }
}

function saveConfig(cfg) {
  try {
    fs.mkdirSync(path.dirname(CONFIG_FILE), { recursive: true });
    fs.writeFileSync(CONFIG_FILE, JSON.stringify(cfg, null, 2));
  } catch {}
}

function parsePort(v) {
  const port = Number(v);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(`Invalid port: ${v}`);
  return port;
}

async function prompt() {
  const cfg = loadConfig();
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const ask = async (q, def) => (await rl.question(def ? `${q} [${def}]: ` : `${q}: `)).trim() || def || "";
  console.log("MultiViewer Replay Sync\n");
  const mode = (await ask("Host a room or join one? (host/join)", cfg.mode ?? "join")).toLowerCase().startsWith("h")
    ? "host"
    : "join";
  const name = await ask("Your name", cfg.name ?? defaultName());
  let target;
  if (mode === "join") {
    while (!target) target = await ask("Host's IP address (e.g. 10.147.17.5)", cfg.target);
  }
  const password = await ask(mode === "host" ? "Room password (optional, Enter for none)" : "Room password (Enter if none)");
  rl.close();
  saveConfig({ mode, name, target: target ?? cfg.target }); // the password is deliberately not saved
  return { mode, name, target, password };
}

function printAddresses(port) {
  const found = [];
  for (const [iface, addrs] of Object.entries(os.networkInterfaces())) {
    for (const a of addrs ?? []) {
      if (a.family === "IPv4" && !a.internal) found.push({ iface, ip: a.address, zt: /zerotier/i.test(iface) });
    }
  }
  found.sort((a, b) => Number(b.zt) - Number(a.zt));
  console.log("\nTell your friends to join with:");
  if (!found.length) console.log("  (no network address found - are you connected?)");
  for (const { iface, ip, zt } of found) {
    console.log(`  ${ip}${port === DEFAULT_PORT ? "" : ":" + port}   ${zt ? "<- ZeroTier " : ""}(${iface})`);
  }
  console.log(`If they can't connect, allow TCP port ${port} through your firewall (see README).\n`);
}

async function main() {
  let values, positionals;
  try {
    ({ values, positionals } = parseArgs({
      allowPositionals: true,
      options: {
        port: { type: "string" },
        name: { type: "string" },
        password: { type: "string" },
        player: { type: "string" },
        help: { type: "boolean", short: "h" },
        version: { type: "boolean", short: "v" },
      },
    }));
  } catch (e) {
    console.error(`${e.message}\n`);
    console.log(USAGE);
    process.exit(1);
  }

  const [command, target] = positionals;
  let mode, name, password;
  if (values.version) {
    console.log(process.env.MVRS_VERSION ?? "dev");
    return;
  }
  if (values.help || (command && command !== "host" && command !== "join")) {
    console.log(USAGE);
    return;
  }
  if (!command) {
    ({ mode, name, target: values.target, password } = await prompt());
  } else {
    mode = command;
    name = values.name ?? defaultName();
    password = values.password;
    values.target = target;
  }
  name = cleanName(name);

  const common = { name, password: password || undefined, playerId: values.player, log, onFatal: fatal };
  if (mode === "host") {
    const port = parsePort(values.port ?? DEFAULT_PORT);
    const { hostToken } = startServer({ port, password: common.password, log, onFatal: fatal });
    printAddresses(port);
    await runClient({ ...common, url: `ws://127.0.0.1:${port}`, hostToken });
  } else {
    if (!values.target) {
      console.log(USAGE);
      process.exit(1);
    }
    await runClient({ ...common, url: roomUrl(values.target) });
  }
}

main().catch((e) => fatal(e.message));
