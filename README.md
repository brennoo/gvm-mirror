# gvm-mirror

Mirrors the container images the [go-gmp](https://github.com/brennoo/go-gmp) integration stack
depends on into `ghcr.io/brennoo/gvm-mirror/*`, a registry this project controls.

A content digest alone only protects against a tag moving — it does nothing if the upstream
registry (Greenbone's) garbage-collects a blob, changes retention, removes a repository, or is
simply unreachable. See [go-gmp's review doc](https://github.com/brennoo/go-gmp/blob/main/docs/review-2026-08.md)
for the full design rationale.

## The lock file

`image-mirror.lock.json` is the durable record of what was mirrored from where, and the seed the
next run reads to know what to re-mirror. go-gmp keeps a reviewed vendored copy at
`hack/image-mirror.lock.json`, synced by a human — see "Syncing go-gmp" below.

## Running it

Requires `skopeo` on `PATH` and a registry session already authenticated for push
(`skopeo login ghcr.io` or `docker login ghcr.io`).

```sh
./mirror-images.sh
```

This re-resolves each image from the channel its lock entry recorded, copies anything new with
Skopeo, and rewrites the lock file in place. Nothing does this automatically or on a schedule
outside `.github/workflows/mirror-images.yml`'s weekly run, which never commits — review the diff
like any other dependency bump before committing it.

### Tag policy

Every image gets a `sha256-<hex>` fallback tag, plus `version`/`revision` tags derived from
`org.opencontainers.image.version`/`.revision` labels when present. All of these are **write-once**:
an existing tag pointing at different content fails the run rather than moving silently.

`-allow-move nightly,main,latest` (baked into `mirror-images.sh`) is the one exception: Greenbone's
nightly images carry the channel name itself as the version label, so `:nightly`/`:main`/`:latest`
are channel aliases, not versions, and are expected to move. A `version` or `revision` tag that
collides with different content — a nightly rebuild reusing its own revision label — is not covered
by this and fails closed; that's a real upstream provenance anomaly, not something to paper over.

## Syncing go-gmp

go-gmp never fetches from this repo at build or runtime. After a mirror run changes the lock, sync
the vendored copy and regenerate go-gmp's Compose pins in one reviewed commit:

```sh
cp image-mirror.lock.json <go-gmp>/hack/image-mirror.lock.json
go run github.com/brennoo/gvm-mirror/cmd/mirrorimages@latest \
  -rewrite-compose-only -lock-file <go-gmp>/hack/image-mirror.lock.json -compose-file <go-gmp>/hack/docker-compose.yml
```

`-rewrite-compose-only` touches neither the registry nor the lock file — it only rewrites Compose
pins from an already-resolved lock.
