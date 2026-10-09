#!/usr/bin/env bash
# Linux (and macOS from a terminal): ./start.sh   - or ./start.sh host / ./start.sh join <ip>
set -u
cd "$(dirname "$0")"
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin" # Homebrew, in case Finder didn't load the shell profile

if ! node -e 'process.exit(+process.versions.node.split(".")[0] >= 20 ? 0 : 1)' >/dev/null 2>&1; then
  echo "Node.js 20 or newer is required. Run ./install.sh first."
  exit 1
fi

# (Re)install dependencies when they're missing or out of date, e.g. after an update
if ! npm ls --omit=dev >/dev/null 2>&1; then
  echo "Installing dependencies..."
  npm install --omit=dev --no-audit --no-fund || { echo "Install failed."; exit 1; }
  echo
fi

exec node src/cli.js "$@"
