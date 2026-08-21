#!/bin/sh
# Mirrors every image image-mirror.lock.json's previous run recorded into
# ghcr.io/brennoo/gvm-mirror using Skopeo, updates that lock file with the
# result, and is the single place tag-policy flag values live so a local run
# and CI can't diverge on them: nightly/main/latest are upstream channel
# aliases allowed to move; data-objects and report-formats rebuild in place
# under an unchanged git revision, so their revision tags may move too, and
# data-objects gets a per-build net.greenbone.feed.version tag instead.
#
# Requires `skopeo` on PATH and a registry session already authenticated for
# push (e.g. `skopeo login ghcr.io` or `docker login ghcr.io`).
#
# This is the only sanctioned way the lock file moves: nothing pushes to
# main automatically. CI opens an auto-approved PR on drift; a human merges.
#
# Usage: ./mirror-images.sh [flags]
set -eu

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_DIR"
exec go run ./cmd/mirrorimages \
	-allow-move nightly,main,latest \
	-allow-move-revision data-objects,report-formats \
	-feed-version-tag data-objects \
	"$@"
