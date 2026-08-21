# gvm-mirror

A digest-pinned mirror of the [Greenbone Community Edition](https://greenbone.github.io/docs/latest/)
container images, published at `ghcr.io/brennoo/gvm-mirror/*`.

## Why

Greenbone's community registry (`registry.community.greenbone.net`) only publishes mutable channel
tags — `stable`, `latest`, `nightly` — and rebuilds or garbage-collects images behind them. That
breaks reproducible deployments in two ways:

- **You can't pin.** There are no version tags upstream; a digest you pin today may be
  garbage-collected tomorrow, and yesterday's image is gone once the channel moves.
- **You can't audit.** When a channel tag moves there is no record of what it pointed at before,
  or whether the move was a release, a rebuild, or something else.

This mirror re-resolves each upstream channel on every run, copies new content byte-for-byte
(`skopeo copy --all --preserve-digests`), and publishes it under tags that never silently change.
Every digest the mirror ever saw stays available and recorded in a reviewed lock file.

## Using the mirror

```sh
docker pull ghcr.io/brennoo/gvm-mirror/gvmd:v26.36.1
```

Pin by digest for reproducibility; the version tags exist so humans can read a pin and know what
it is. Manifests are copied byte-for-byte, so a mirrored digest can always be compared directly
against upstream's.

## Mirrored images

All images are mirrored from `registry.community.greenbone.net/community/<name>` for
`linux/amd64` and `linux/arm64`.

| Image | Upstream channel | What it is |
|---|---|---|
| `gvmd` | `stable` | Greenbone Vulnerability Manager — the central daemon and database backend |
| `gsa` | `stable` | Greenbone Security Assistant — the web UI |
| `openvas-scanner` | `stable` | The OpenVAS scan engine |
| `ospd-openvas` | `stable` | OSP server that lets gvmd remotely control an OpenVAS scanner |
| `pg-gvm` | `stable` | PostgreSQL extension with gvmd helper functions |
| `gvm-tools` | `latest` | CLI tools (`gvm-cli`, `gvm-script`, `gvm-pyshell`) for scripting GMP/OSP |
| `redis-server` | `latest` | Redis configured as the scanner's knowledge-base store |
| `gpg-data` | `latest` | Greenbone feed-signing GPG keyring, for feed signature verification |
| `vulnerability-tests` | `latest` | The NASL vulnerability tests (VTs) — the scanner's feed |
| `notus-data` | `latest` | Notus data for local security checks (package-based detection) |
| `scap-data` | `latest` | SCAP feed data (CVEs, CPEs) loaded by gvmd |
| `cert-bund-data` | `latest` | CERT-Bund (German BSI) security advisories |
| `dfn-cert-data` | `latest` | DFN-CERT security advisories |
| `data-objects` | `latest` | Scan configs, policies, and port lists from the Greenbone feed |
| `report-formats` | `latest` | gvmd's built-in report format definitions |

## How tags work

Each mirrored image gets up to four kinds of tags:

- **`sha256-<hex>`** — one per manifest digest ever mirrored. Write-once, never moves, never
  deleted: the permanent record.
- **Version** — from the `org.opencontainers.image.version` label (e.g. `gvmd:v26.36.1`).
  Write-once, except when the label is a channel alias (`nightly`, `main`, `latest`), which
  tracks the channel and moves with it.
- **Revision** — from the `org.opencontainers.image.revision` label: the git commit the image
  was built from. Write-once, except for `data-objects` and `report-formats`, whose upstreams
  rebuild the same commit in place; their revision tag follows the latest rebuild.
- **Feed version** — `data-objects` only: its `net.greenbone.feed.version` label (e.g.
  `202608210512-enterprise`), the per-build feed version its moving revision no longer pins.

Images whose version/revision labels are missing or disagree across platforms (currently
`dfn-cert-data`, `ospd-openvas`) get only the `sha256-` tag.

Any other tag colliding with different content fails the run rather than moving — that's a real
upstream provenance anomaly, not something to paper over.

## The lock file

`image-mirror.lock.json` records, for every image: the upstream channel it was resolved from, the
source and destination digests, resolved version/revision, tags, and platforms. It is both the
audit trail and the seed the next run reads to know what to mirror. Changes to it land via
reviewed PRs, never direct pushes — `.github/workflows/mirror-images.yml` runs weekly and opens
an auto-approved PR when the lock drifts.

## Running it

Requires `skopeo` on `PATH` and a registry session authenticated for push
(`skopeo login ghcr.io`).

```sh
./mirror-images.sh
```

The script is the single home of the tag-policy flags (`-allow-move`, `-allow-move-revision`,
`-feed-version-tag`) so a local run and CI can't diverge on them. See `cmd/mirrorimages -h` for
all flags, including `-dry-run` and `-dest-prefix` if you want to run your own mirror.

## Pinning a Compose file

`mirrorimages` can rewrite a Docker Compose file's `image:` lines to the mirror's pinned digests,
from a lock file alone — no registry access:

```sh
go run github.com/brennoo/gvm-mirror/cmd/mirrorimages@latest \
  -rewrite-compose-only -lock-file image-mirror.lock.json -compose-file docker-compose.yml
```

Each matching line becomes `image: <digest-pin> # <version>`. This is how downstream projects
(e.g. [go-gmp](https://github.com/brennoo/go-gmp)) keep their Compose stacks pinned to reviewed
digests.
