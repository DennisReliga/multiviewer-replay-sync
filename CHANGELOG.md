# Changelog

## 0.2.0

- Rewritten in Go: downloads are about 7 MB instead of 100 MB.
- New downloads for Intel Macs, Windows on ARM and Linux on ARM.
- Connections from web browsers are refused, so a web page can't join a room.
- Removed the Node.js launchers and setup scripts; the downloads replace them.
- Same features and network protocol as 0.1.0.

## 0.1.0 — first public release

- Host/join rooms over your LAN, ZeroTier or any similar network where friends can reach the host.
- Everyone in the room can pause, resume and seek; it is mirrored to all.
- Follows MultiViewer's main feed; pause/resume reaches onboard windows, and MultiViewer's own sync
  is triggered after every change so onboards and live timing line up again.
- Optional room password.
- Standalone downloads for Windows, macOS (Apple Silicon) and Linux — no Node.js needed.
- Launchers and setup scripts for running from source on Windows, macOS and Linux.
