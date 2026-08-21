#!/bin/sh
# Mirrors every image image-mirror.lock.json's previous run recorded into
# ghcr.io/brennoo/gvm-mirror using Skopeo, updates that lock file with the
# result, and is the single place -allow-move's flag value lives so a local
# run and CI can't diverge on it: nightly/main/latest are upstream channel
# aliases, not versions, so they're the only tags allowed to move.
#
# Requires `skopeo` on PATH and a registry session already authenticated for
# push (e.g. `skopeo login ghcr.io` or `docker login ghcr.io`).
#
# This is the only sanctioned way the lock file moves: nothing commits its
# changes automatically. Run it, review the diff, and commit deliberately.
#
# Usage: ./mirror-images.sh [flags]
set -eu

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_DIR"
exec go run ./cmd/mirrorimages -allow-move nightly,main,latest "$@"
