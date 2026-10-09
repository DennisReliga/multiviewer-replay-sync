#!/usr/bin/env bash
# macOS: double-click in Finder. Opens Terminal and runs start.sh.
cd "$(dirname "$0")"
bash ./start.sh "$@"
echo
read -r -p "Press Enter to close..."
