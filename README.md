# MultiViewer Replay Sync

Watch F1 replays in [MultiViewer](https://multiviewer.app) together with friends — everyone's playback stays in sync over your LAN, [ZeroTier](https://www.zerotier.com) or a similar virtual network.

> **Unofficial.** This is a community project. It is not affiliated with, endorsed by or connected to MultiViewer, Formula 1, F1 TV or the Formula One group of companies. It does not share any video: everyone watches with their own F1 TV subscription.

## What it does

- **Everyone has controls.** Anyone pauses, resumes or seeks — the whole room follows.
- **Onboards and live timing too.** It follows your main feed, pauses your onboard windows with it, and runs MultiViewer's own sync after every change so onboards and timing line up again.
- **Self-correcting.** If someone's stream drifts more than 1.5 s, they're quietly moved back in line.
- **Safe to join late.** New people jump straight to where the room is.
- **Optional room password.**

Replays only — live streams can't be seeked, so there's nothing to sync.

## Setup

Everyone needs MultiViewer, an F1 TV account, and this app.

### Easiest: download the app

Get the file for your system from the **[latest release](../../releases/latest)** — no install, nothing else needed:

| System | File | Start |
|---|---|---|
| **Windows** | `…-windows-x64.exe` | double-click |
| **macOS** (Apple Silicon) | `…-macos-arm64.zip` | unzip, double-click |
| **Linux** | `…-linux-x64.tar.gz` | unpack, run `./multiviewer-replay-sync` |

The files aren't code-signed, so the first start shows a warning:
- **Windows:** *"Windows protected your PC"* → **More info → Run anyway**.
- **macOS:** if it's blocked → **System Settings → Privacy & Security → Open Anyway**.

### Alternative: run from source

For Intel Macs, or if you'd rather not run a downloaded executable. Download the code (green **Code** button → *Download ZIP*, or `git clone`), then:

| | Install (once) | Start |
|---|---|---|
| **Windows** | double-click `install.bat` | double-click `start.bat` |
| **macOS** | in Terminal: `bash install.sh` | double-click `start.command` |
| **Linux** | `bash install.sh` | `./start.sh` |

The installer sets up [Node.js](https://nodejs.org) 20+ if you don't have it (it asks first). On macOS, if double-clicking `start.command` is blocked, right-click it → *Open* once.

## Watching together

1. Everyone opens **the same replay** in MultiViewer.
2. **One person hosts**: start the app and choose `host`. It prints the addresses friends can use — on ZeroTier, share the one marked `<- ZeroTier`.
3. **Everyone else joins**: start the app, choose `join` and enter the host's address.
4. Watch. Pause, rewind, skip — everyone stays together.

The app remembers your name and the host's address for next time; just press Enter to keep them.

### Command line

```
start.bat host [--port 8765] [--password secret]
start.bat join 10.147.17.5 [--password secret]
./start.sh join [fd00::1]:8765
```

Options: `--name <name>`, `--password <text>`, `--player <id>` (which MultiViewer window to follow; default is the main feed), `--port`, `--help`.

### Letting friends connect (host only)

The host's computer must accept connections on TCP port **8765**.

- **Windows:** `install.bat` offers to add the rule. To do it by hand, in an *administrator* PowerShell:
  ```powershell
  New-NetFirewallRule -DisplayName "MultiViewer Replay Sync" -Direction Inbound -Protocol TCP -LocalPort 8765 -Action Allow -InterfaceAlias "ZeroTier*"
  ```
  Using a normal LAN or another virtual network instead? Swap `"ZeroTier*"` for that adapter's name (see `Get-NetAdapter`). Remove it again with `Remove-NetFirewallRule -DisplayName "MultiViewer Replay Sync"`.
- **macOS:** allow the incoming connection when macOS asks.
- **Linux:** with ufw, `sudo ufw allow 8765/tcp`.

Friends can test whether they get through (Windows): `Test-NetConnection <host-ip> -Port 8765`.

### Security

Anyone who can reach the host's port can join the room and control playback, so keep it on a private network (home LAN, ZeroTier or similar) and **don't forward the port on your router**. Set a `--password` if you share the network with people outside your group. The connection itself is not encrypted, but ZeroTier and similar virtual networks encrypt all traffic between peers. The password is never saved to disk.

## How it works

MultiViewer has a local API (`localhost:10101`). Each person's app reads their playback position from it and controls pause/seek/sync through it. The host's app also runs a tiny relay that holds the room's shared state: when anyone pauses, resumes or seeks, their app reports it, and everyone else's app applies it. Only these small control messages travel over the network.

## Troubleshooting

- **"MultiViewer not reachable"** — start MultiViewer first.
- **"No MultiViewer player open"** — open the replay.
- **"Different session open than the room"** — you're on a different replay (or session) than the host.
- **"Can't reach … is the host running?"** — check the host's address, that the host app is running, and the firewall rule.
- **"Wrong room password"** — ask the host for it.

## Development

```
npm install
npm test     # host + joiners against fake MultiViewer players, no real app needed
npm run build   # standalone executable for this OS in dist/ (needs Node 25.5+)
```

Releases: push a tag like `v0.2.0`. GitHub Actions builds the executables for all three systems, tests them, and publishes the release.

Code: [`src/cli.js`](src/cli.js) (prompts/options), [`src/client.js`](src/client.js) (sync logic), [`src/server.js`](src/server.js) (room relay), [`src/multiviewer.js`](src/multiviewer.js) (MultiViewer API), [`src/protocol.js`](src/protocol.js) (message validation).

## License

[MIT](LICENSE)
