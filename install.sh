#!/usr/bin/env bash
# macOS / Linux setup: installs Node.js if needed (asks first), then the app's dependency.
set -euo pipefail
cd "$(dirname "$0")"
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"

node_ok() { node -e 'process.exit(+process.versions.node.split(".")[0] >= 20 ? 0 : 1)' >/dev/null 2>&1; }

confirm() {
  read -r -p "$1 [y/N] " answer
  [[ "$answer" =~ ^[Yy] ]]
}

echo "MultiViewer Replay Sync - setup"
echo

if ! node_ok; then
  echo "Node.js 20 or newer is needed and was not found."
  cmd=()
  case "$(uname -s)" in
    Darwin)
      if command -v brew >/dev/null; then cmd=(brew install node); fi
      ;;
    Linux)
      if command -v apt-get >/dev/null; then cmd=(sudo apt-get install -y nodejs npm)
      elif command -v dnf >/dev/null; then cmd=(sudo dnf install -y nodejs npm)
      elif command -v pacman >/dev/null; then cmd=(sudo pacman -S --needed nodejs npm)
      fi
      ;;
  esac
  if [ ${#cmd[@]} -eq 0 ]; then
    echo "Please install Node.js 20+ from https://nodejs.org and run this again."
    exit 1
  fi
  if ! confirm "Install it now with: ${cmd[*]} ?"; then
    echo "Cancelled. You can also install it from https://nodejs.org"
    exit 1
  fi
  "${cmd[@]}"
  if ! node_ok; then
    echo
    echo "Your package manager installed Node.js $(node -v 2>/dev/null || echo '?'), which is too old."
    echo "Install Node.js 20+ from https://nodejs.org or with nvm (https://github.com/nvm-sh/nvm)."
    exit 1
  fi
fi

echo "Installing dependencies..."
npm install --omit=dev --no-audit --no-fund
chmod +x start.sh start.command 2>/dev/null || true

echo
echo "All set. Start the app with ./start.sh (Linux) or by double-clicking start.command (macOS)."
if [ "$(uname -s)" = "Linux" ] && command -v ufw >/dev/null; then
  echo "Hosting? If your firewall (ufw) is on, allow the port:  sudo ufw allow 8765/tcp"
fi
