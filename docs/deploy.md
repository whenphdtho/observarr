# Deployment: host and container figures

Observarr always works with nothing more than its data directory. Three independent, optional sources add the
System view's host and container figures; each is a read-only mount or a read-only Docker API, and whichever
you leave out, Observarr says so in the System view instead of failing.

| Source | What it enables | Needs |
|---|---|---|
| The host's `/proc`, read-only | CPU %, memory, swap, load average, uptime, the host's traffic | `OBSERVARR_HOST_PROC` |
| Disk paths | Disk usage bars | `OBSERVARR_HOST_DISKS` (comma-separated) |
| The host's `/sys/fs/cgroup`, read-only | Per-container CPU %, memory, block I/O and traffic | `OBSERVARR_HOST_PROC` + `OBSERVARR_HOST_CGROUP` + `cgroup: host` on the Observarr container (below) |
| A Docker socket proxy | Container list, state, health, restarts, lifecycle events, and listing your app containers on the Add apps page | `OBSERVARR_DOCKER_HOST` |

None of this needs a privileged container or write access to anything: every mount below is `:ro`, and the
socket proxy only ever answers GET requests (Observarr's own Docker client issues no other kind). Read
"Security notes" below before turning any of it on — a read-only mount still *exposes* what it mounts to
whoever can reach the Observarr container, which is a real cost even though nothing here can be used to change
anything.

## Compose example

This also lives at [`deploy/docker-compose.yml`](../deploy/docker-compose.yml) — copy it and adjust the data
disk(s) and (if you use it) the app config mounts. It runs the published image; to build from source instead,
swap in the commented `build: ..` lines (`deploy/` is one level below the repo root). Which tag to use and how
to upgrade: [Releases and versions](#releases-and-versions).

```yaml
services:
  observarr:
    # :0 follows the newest 0.x release. To pin one exact build instead, use its digest from the release
    # notes: ghcr.io/whenphdtho/observarr@sha256:<digest> (see "Releases and versions" in ../docs/deploy.md).
    image: ghcr.io/whenphdtho/observarr:0
    # To build from source instead, replace the line above with these two (`up -d --build` rebuilds):
    # build: ..
    # image: observarr:local
    restart: unless-stopped
    # Hardening that costs nothing here: Observarr never writes outside /data (a volume, unaffected
    # by read_only) and needs no Linux capability or privilege escalation.
    read_only: true
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp   # some of SQLite's own temp files still want a writable /tmp even with read_only above
    # A dedicated uid, not the default 65532 every distroless "nonroot" image shares — see "Security
    # notes" below. chown ./data (and any host mount uid 64000 needs to read) before uncommenting.
    # user: "64000:64000"
    # The container's own cgroup namespace must be the host's: a container's cgroup path (read from its
    # /proc/<pid>/cgroup) is then the same path the mounted /host/sys/fs/cgroup uses, so Observarr can find it.
    # Without this, container CPU/memory/I/O stay unavailable even with the mounts below.
    cgroup: host
    ports:
      - "9898:9898"
    networks:
      - default    # Compose's own network for this file — join your apps' existing network too (see
                   # "Adding your apps" in docs/configuration.md) if you want to reach them from here
      - internal    # reach the socket proxy, and nothing else
    environment:
      - OBSERVARR_HOST_PROC=/host/proc
      - OBSERVARR_HOST_CGROUP=/host/sys/fs/cgroup
      - OBSERVARR_HOST_DISKS=/host/media
      - OBSERVARR_DOCKER_HOST=tcp://socket-proxy:2375
      - TZ=Europe/Berlin   # daily traffic totals begin at local midnight
      # - OBSERVARR_TRAFFIC_BUDGET=20TB/month   # optional: show how much of a traffic budget is used
      # - OBSERVARR_INSECURE_COOKIES=1   # only for plain HTTP on a trusted LAN; leave unset behind an HTTPS reverse proxy
      # Optional: lists apps on the Add apps page from their own config files (see "Adding your apps" in docs/configuration.md).
      # Needs one read-only mount per app below.
      # - OBSERVARR_APP_CONFIGS=/appconfig
    volumes:
      - ./data:/data
      # Read-only, but every process's environment variables and open files are visible under here for
      # every process on the host (e.g. /host/proc/<pid>/environ, /host/proc/<pid>/root), not just the
      # handful of aggregate files Observarr's own code reads — see "Security notes" for a narrower
      # alternative if you only want host figures, not per-container ones.
      - /proc:/host/proc:ro
      - /sys/fs/cgroup:/host/sys/fs/cgroup:ro
      # One or more data disks to show in the System view; list each one here and in
      # OBSERVARR_HOST_DISKS above.
      - /mnt/media:/host/media:ro
      # /:/host/root:ro covers the whole root filesystem read-only instead — every other app's data on
      # it, and the path the Docker socket itself lives at, not just a disk's usage. Avoid it; see
      # "Security notes".
      # - /:/host/root:ro
      # - /srv/sonarr/config:/appconfig/sonarr:ro   # one such mount per app, only with OBSERVARR_APP_CONFIGS

  socket-proxy:
    # latest (v0.6.0 deprecated the versioned tags in favor of latest/lts/3.4), pinned by digest:
    # this image reads the Docker socket, so a re-pushed tag must not change it unseen
    image: tecnativa/docker-socket-proxy@sha256:be1ac64f33bd82043beb504822a17ba6b71668ea1ad7cf51a9a01d27bb7ab047
    restart: unless-stopped
    environment:
      - CONTAINERS=1   # container list, inspect and (see below) more than that
      - EVENTS=1       # the lifecycle event stream (start/stop/health changes refresh the list promptly)
      - VERSION=1      # one call at startup to negotiate the API version
      # Other endpoints are refused because their own variables default to 0 when unset; POST=0 is
      # listed anyway, for emphasis, since it's the one a write would need.
      - POST=0
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks:
      - internal    # no other network, and no published port: nothing outside this pair can reach it

networks:
  internal:
    internal: true   # Docker refuses this network any route to the outside world, published ports included
```

## Releases and versions

Each release is a `vX.Y.Z` tag. It publishes a multi-arch image (linux/amd64 and linux/arm64) to
`ghcr.io/whenphdtho/observarr`, signed with cosign and carrying SBOM and provenance attestations, plus a GitHub
release with static binaries (linux/amd64, linux/arm64, darwin/arm64) and a `SHA256SUMS` file.

| Tag pushed | Image tags |
|---|---|
| `v1.4.2` | `1.4.2`, `1.4`, `1`, `latest` |
| `v1.5.0-rc.1` (prerelease) | `1.5.0-rc.1` only |

`1.4` follows patch releases, `1` follows minor ones too, and `latest` follows everything that is not a
prerelease. While the version is 0.x, a minor release may change behaviour, so `:0` moves faster than a later
`:1` will; pin `0.Y` if you want only fixes.

**Upgrading.** Pull and recreate the container:

```sh
docker compose -f deploy/docker-compose.yml pull
docker compose -f deploy/docker-compose.yml up -d
```

When a new version changes the database schema, Observarr first copies the database to
`<data>/backups/observarr-<time>-pre-migrate-<N>.db` (the newest 5 are kept) and only then migrates, each step
in one transaction. Going back to an older version afterwards needs that copy: an older Observarr refuses a
database newer than it knows. Stop the container, replace `observarr.db` with the backup (and remove
`observarr.db-wal` and `observarr.db-shm`), then start the older image. Keep `secret.key` with any copy you
take; see [Recovery](configuration.md#recovery).

**Pinning by digest.** A tag can move; a digest cannot. Each release's notes list the image digest, and
`docker buildx imagetools inspect ghcr.io/whenphdtho/observarr:1.4.2` shows it too. Use it as
`image: ghcr.io/whenphdtho/observarr@sha256:<digest>` (you can keep the tag in a comment beside it for
readability). Check that an image came from this repository's release workflow with:

```sh
cosign verify ghcr.io/whenphdtho/observarr:<tag> \
  --certificate-identity-regexp 'https://github.com/whenphdtho/observarr/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## cgroup v1

Container CPU, memory and block I/O need cgroup v2 (the unified hierarchy almost every current distribution
ships by default). A cgroup v1 host still gives you the container list, state, health, restarts and events
through the socket proxy — the System view shows a notice explaining the figures it can't provide instead of
guessing at them. Check with `stat -fc %T /sys/fs/cgroup` on the host: `cgroup2fs` is v2; `tmpfs` is v1.

## System view notes

- **A disk path that stops answering** (a hung network mount, an unresponsive filesystem) shows "Disk `<path>`
  is not responding" and keeps showing its last good figures instead of blocking the rest of the sample —
  disks are measured in the background, independently of host CPU/memory/load.
- **More than 1000 containers are cut off**: the System view shows "Containers: only the first 1000 are
  shown" past that count, sorted the same way the table is.
- **The container list refreshes at most every 5 seconds**, even during a burst of Docker lifecycle events
  (several containers starting or stopping at once); host figures (CPU, memory, …) keep sampling on their own
  schedule regardless.

## Network view notes

- **The host's traffic** is the sum of its physical interfaces in the mounted `/proc` (loopback, `veth*`,
  `docker*` and `br-*` are left out), so it is what actually crosses the host's network cards.
- **Per-app traffic** needs the container figures above (`OBSERVARR_HOST_PROC`, `OBSERVARR_HOST_CGROUP`, the
  socket proxy and `cgroup: host`): each running container's counters are read from its own network namespace
  via `/proc/<pid>/net/dev`. Containers with `network_mode: host` or `network_mode: container:…` have no
  counters of their own and count only towards the host. Containers are grouped under the service they are
  linked to (the Container field of a service).
- **Other** is the host's traffic that no shown app explains: the host itself, system updates, containers on
  the host network, and the apps beyond the five largest. It never goes below zero. App figures also include
  traffic between containers, which never leaves the host, so the apps together can briefly add up to more
  than the host's total.
- **Daily totals** are counted from midnight in the container's time zone: set `TZ` (e.g. `TZ=Europe/Berlin`),
  otherwise days begin at midnight UTC. They are written once a minute and kept for 35 days.
- **History:** every minute the host's traffic, each app's traffic and the host's CPU and memory are written
  to the database, and added up into hours and local days (same `TZ`). The day chart reads the last day back
  after a restart; the week and month charts use hours, the year chart days. A stretch when Observarr was not
  running stays a gap, and a change against the period before is shown only when at least 80% of that period
  was recorded. The five-minute chart is live only and starts empty after a restart. If the database is slow,
  up to two hours of minutes wait in memory and the oldest are dropped beyond that; a minute that fails to be
  written five times in a row is dropped too (both logged once).
- **Budget:** `OBSERVARR_TRAFFIC_BUDGET=1400GB/day` or `=20TB/month` (decimal units: 1 GB = 10^9 bytes)
  counts down and up together, the way most providers do; a monthly budget adds up the calendar month's days.
- **Units:** rates are in Mbit/s and totals in GB/TB, as providers count; memory and disks stay in binary
  units (GiB).

## Storage

The Storage view shows the disks listed in `OBSERVARR_HOST_DISKS` (used, reserved and free space; two paths on
the same file system are counted once in the totals) and the mounts of every service that reports them. The
System view keeps a short disk summary that links there.

### Connect rclone

Observarr reads rclone's mounts, VFS caches and transfers through rclone's remote-control API. It only makes
read-only calls (`core/version`, `core/stats`, `rc/noopauth`, `mount/listmounts`, `vfs/list`, `vfs/stats`); rclone
takes them as POST requests, but none of them changes anything, and the adapter refuses any other call.

1. **Turn the remote control on.** Either start your mount with it:

   ```sh
   rclone mount media-crypt: /mnt/media --vfs-cache-mode full \
     --rc --rc-addr 0.0.0.0:5572 --rc-user observarr --rc-pass 'a long random password'
   ```

   or run a separate `rclone rcd --rc-addr 0.0.0.0:5572 --rc-user … --rc-pass …` and create the mounts through
   it. `0.0.0.0` makes the port reachable from other containers; publish it on no host port.
2. **Keep the port on an internal network.** The remote control can also change things — unmount, delete,
   copy — for whoever holds the credentials, so put rclone and Observarr on a Docker network of their own
   (`internal: true` where rclone needs no other access through it) and never expose 5572 to the internet.
3. **Add it in Observarr:** Apps → Add apps → Add manually → rclone, URL `http://rclone:5572` (the container
   name and the `--rc-addr` port), and the credentials as `user:password` (`observarr:a long random password`). They are
   sealed like an API key and never shown again. Leave them blank only with `--rc-no-auth`.

Notes:

- **Mount points.** `mount/listmounts` lists only mounts made through the remote control (`rclone rcd` plus
  `mount/mount`). A mount started on the command line still shows, with its cache and uploads, but as *active*
  with "mount point not reported".
- **Without credentials** (and without `--rc-no-auth`), rclone refuses `mount/listmounts`; everything else
  still works. With `--rc-user` set, every call needs the credentials.
- **Cache figures** are those of the VFS disk cache (`--vfs-cache-mode` above `off`): used bytes against
  `--vfs-cache-max-size` (no limit when unset), cached files, uploads in progress and queued, files that failed
  to upload, and whether the cache disk is out of space. The last two also show as warnings on the service.
- AltMount is not supported: it has no documented read-only API for its mount status that works without a
  logged-in session.

## Memory

Observarr idles at a few tens of MiB. Go's garbage collector honours `GOMEMLIMIT` on its own; set it if the
host is short on memory, so the heap is collected harder before it grows past the limit:

```yaml
    environment:
      - GOMEMLIMIT=128MiB
```

It is a soft limit: Observarr does not fail when it is reached, it only collects garbage more often. Each
password check briefly takes 19 MiB (one per CPU at most at the same time), so a hard limit (`mem_limit`) should
leave room for that — 256 MiB is plenty.

## Connect CrowdSec

The Security view always shows how Observarr itself is protected. With CrowdSec connected it also shows what
CrowdSec blocks: the active decisions (bans, captchas), how many were made on your machine and how many come
from the community blocklist or subscribed lists, the top scenarios and the latest local decisions.

Observarr reads CrowdSec's Local API (LAPI) the way a bouncer does, read-only:

1. Create a bouncer key for Observarr on the CrowdSec host (or in its container):

   ```sh
   cscli bouncers add observarr
   # in Docker: docker exec crowdsec cscli bouncers add observarr
   ```

   It prints the key once; copy it. `cscli bouncers delete observarr` revokes it again.
2. Give Observarr network access to the LAPI (port 8080 by default). In Compose, put both on a shared network
   and use the service name, e.g. `http://crowdsec:8080`. The LAPI only needs to be reachable by Observarr,
   never from the internet.
3. In Observarr, **Apps → Add apps → Add manually**, pick CrowdSec, enter the URL and the bouncer key, **Test**, then
   **Add**.

What Observarr does with the key: it calls `GET /v1/decisions` (every active decision, about once a minute
while someone is watching) and `GET /v1/decisions?ip=127.0.0.1` as a quick health check. A bouncer key can only
read decisions; it cannot add or remove any. Observarr keeps only counts and the 50 newest local decisions;
community blocklists with tens of thousands of entries are counted while they are read, never stored. Past
about 48 MiB of decisions counting stops and the totals are shown as a lower bound.

CrowdSec's alerts (the events behind each decision, with source country and AS) need *machine* credentials,
which can also write to the LAPI. Observarr does not ask for them, so alerts are out of scope. The LAPI does not
tell a bouncer its version, so the card shows none.

## Connect Overseerr or Jellyseerr

Overseerr, Jellyseerr and Seerr (the two merged into it with version 3) share one API, and one adapter reads
all three. Its card names the app that answers, shows its requests by state (waiting, approved, processing,
available, declined, failed), the ten newest requests with their title, type, requester and state, and the
issues users reported that are still open. Failed requests also show up in Needs attention; requests waiting
for approval are noted below it, as good to know.

1. Copy the API key from **Settings → General → API Key**.
2. Give Observarr network access to the app (port 5055 by default), e.g. `http://jellyseerr:5055` on a shared
   Docker network.
3. In Observarr, **Apps → Add apps**: a running `sctx/overseerr`, `lscr.io/linuxserver/overseerr`,
   `hotio/overseerr`, `fallenbagel/jellyseerr` or `ghcr.io/seerr-team/seerr` container is found on its own;
   otherwise **Add manually**, pick Overseerr / Jellyseerr, enter the URL and the key, **Test**, then **Add**.

What Observarr reads, all with GET and about once a minute while someone is watching (the health check every
30 seconds): the status, request counts, the ten newest requests and the open issues. The app's API key can
also change things; Observarr never does. Requests carry no titles of their own, so Observarr asks the app for
each title once (at most two at a time) and remembers it. Requesters are shown by their display name only;
when the app's display name for a user is their e-mail address, the requester stays blank.

## Security notes

- **The socket proxy only ever answers GET requests.** Every other endpoint is refused because its own
  variable (`POST`, `DELETE`, …) defaults to 0 when unset; `POST=0` is listed anyway, for emphasis, since it's
  the one a write would need. That's on top of Observarr's own Docker client never issuing anything but GET —
  two independent reasons the setup can't be used to start, stop or otherwise change a container.
- **`CONTAINERS=1` allows more than listing and inspecting, though.** The same toggle also enables
  `/containers/{id}/archive` and `/containers/{id}/export` — reading or downloading any file out of *any*
  container's filesystem — and `/containers/{id}/json` returns each container's full `Env`, which commonly
  holds secrets (database passwords, API keys of its own). Observarr itself only ever calls list, inspect and
  the event stream; the proxy doesn't hold it to that. If your socket proxy supports a path allow-list, use one
  and restrict it to exactly what Observarr calls, e.g.
  `^/v1\.\d+/(version|containers/json|containers/[0-9a-f]+/json|events)$`.
- **Never expose the socket proxy.** Give it no `ports:`, and put it on a network marked `internal: true` (as
  above) with nothing else attached — not the network your other apps talk to each other on. The proxy's whole
  purpose is standing between Observarr and `/var/run/docker.sock`, which is itself root-equivalent access to
  the host; a reachable proxy is almost as sensitive as the socket it guards.
- **Avoid mounting `/var/run/docker.sock` into Observarr directly.** It would work (`OBSERVARR_DOCKER_HOST=unix:///var/run/docker.sock`),
  and Observarr logs a warning the one time it starts that way, but the socket grants whoever holds it full
  control of the Docker host, not just the read-only access Observarr needs — the proxy is the point.
- **The host `/proc` mount exposes more than command lines.** `/host/proc/<pid>/environ` holds that process's
  environment variables (often secrets) and `/host/proc/<pid>/root` is a view of its entire filesystem, for
  *every* process on the host — not just the ones in containers, and not just the handful of aggregate files
  (`/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime`, `/proc/net/dev`) Observarr's own host-figures
  code actually reads. That per-PID exposure is the real cost of the container figures (CPU/memory/I/O per
  container), which do need the full tree to resolve a container's cgroup path. If you only want the *host*
  figures and not per-container ones, skip `OBSERVARR_HOST_CGROUP` and `cgroup: host` entirely and bind-mount
  just those five files instead of the whole tree:
  ```yaml
      volumes:
        - /proc/stat:/host/proc/stat:ro
        - /proc/meminfo:/host/proc/meminfo:ro
        - /proc/loadavg:/host/proc/loadavg:ro
        - /proc/uptime:/host/proc/uptime:ro
        - /proc/1/net/dev:/host/proc/net/dev:ro   # PID 1 is always in the host's own network namespace
  ```
- **`/:/host/root:ro` exposes the whole host root filesystem, read-only, under `/host/root`** — including every
  other app's data on it and the path the Docker socket (`/var/run/docker.sock`) itself lives at, not just the
  disk usage figures it's there for; read-only doesn't stop a process from *connecting* to a socket file it can
  reach, only from modifying it. Use a narrower, data-disk-only mount instead (e.g. `/mnt/media:/host/media:ro`,
  with a matching entry in `OBSERVARR_HOST_DISKS`, as the compose example above now does by default).
- **Observarr runs as uid 65532** (a distroless, non-root image) — the *same* default uid every image built
  `FROM gcr.io/distroless/*:nonroot` uses, Observarr's included. If anything else on the host (or in another
  container sharing a host path) also runs as 65532, the two share whatever file access that uid has, which
  widens what a compromise of either one reaches. Consider giving Observarr a uid nobody else uses, e.g.
  `user: "64000:64000"` in the compose example above, with `./data` (and any host mount it needs to read)
  `chown`ed to match.
- `read_only: true`, `cap_drop: [ALL]` and `security_opt: [no-new-privileges:true]` (in the compose example
  above) cost nothing: Observarr writes only under `/data` (a volume, unaffected by `read_only`) and needs no
  Linux capability or ability to gain new privileges.
- Every host and cgroup mount above is read-only (`:ro`); nothing here needs `privileged: true` or extra
  capabilities of its own.

## Connect SABnzbd and NZBGet

Observarr shows a downloader's queue with its speed, whether it is paused, and the downloads that failed in the
last day. It only reads; it never pauses, deletes or adds anything.

- **SABnzbd:** Apps → Add apps → Add manually → SABnzbd, URL `http://sabnzbd:8080` (add the URL base if you set
  one, e.g. `http://sabnzbd:8080/sabnzbd`), and the API key from Config → General → Security. The key travels in
  the URL's query string, as SABnzbd requires; Observarr never logs it or shows it in an error. Use `http://`
  only on a network you trust, or HTTPS.
- **NZBGet:** Apps → Add apps → Add manually → NZBGet, URL `http://nzbget:6789`, and the credentials as
  `user:password` from Settings → Security (`ControlUsername`/`ControlPassword`). A restricted user
  (`RestrictedUsername`) is enough and cannot change settings. Leave them blank only if no password is set.

Both are found as Docker containers (`sabnzbd/sabnzbd`, `lscr.io/linuxserver/sabnzbd`, `hotio/sabnzbd`,
`lscr.io/linuxserver/nzbget`, `nzbgetcom/nzbget`) and by a network scan on their default ports. With their config
folders mounted (see [Adding your apps](configuration.md#adding-your-apps)), the key or credentials are offered
too.

## Connect Jellyfin

Observarr shows who is watching what on Jellyfin, how — direct play, direct stream or transcode, with
Jellyfin's reasons for converting — and at what bitrate, on the Overview ("Now playing"), on the Jellyfin card
and next to Jellyfin's traffic in the Network view.

1. **Create an API key:** in Jellyfin, Dashboard → API Keys → **+**, name it `Observarr`. Deleting it there
   revokes it.
2. **Give Observarr network access** to Jellyfin's HTTP port (8096 by default). In Compose, share a network and
   use the container name, e.g. `http://jellyfin:8096`. Docker discovery recognises the `jellyfin/jellyfin`,
   `lscr.io/linuxserver/jellyfin` and `hotio/jellyfin` images, and the network scan finds Jellyfin on port 8096.
   Behind a reverse proxy with a base path, include it: `https://media.example.org/jellyfin`.
3. **Add it:** Apps → Add apps → Add manually → Jellyfin, the URL and the key, **Test**, then **Add**.

What Observarr does with the key: it reads `GET /System/Info` (version, pending restart) and
`GET /Sessions` (what plays now, every 10 s while someone watches, every 2 minutes otherwise) and nothing else.
An API key is an administrator's key in Jellyfin; Observarr only ever reads with it, and keeps it sealed like
any other. The clients' IP addresses that Jellyfin reports with each session are never read or stored.

Emby is not supported.
