#!/usr/bin/env bash
set -euo pipefail

repository_root="$(git rev-parse --show-toplevel)"
git -C "$repository_root" archive \
  --format=zip \
  --output="$repository_root/../yomye-chat.zip" \
  HEAD
