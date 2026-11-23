# Fixtures

Every file here is hand-written from rclone's documented remote-control answers and the source that builds them
(fs/rc/internal.go, fs/accounting/stats.go, cmd/mountlib/rc.go, vfs/rc.go, vfs/vfscache/cache.go). None of it was
captured from a running instance: remote names, paths, file names and figures are invented.

- `core_version.json` — `core/version`.
- `core_stats.json` — `core/stats` with two transfers in progress and two errors.
- `core_stats_idle.json` — `core/stats` with nothing transferring (no `transferring` key).
- `vfs_list.json` — `vfs/list` with two VFSes, `media-crypt:` and `cloud:backup`.
- `vfs_stats_media_crypt.json` — `vfs/stats` for `media-crypt:`, cache mode full (3), 100 GiB cache limit.
- `vfs_stats_cloud_backup.json` — `vfs/stats` for `cloud:backup`, cache mode off (0): no `diskCache`.
- `listmounts.json` — `mount/listmounts` with `media-crypt:` mounted at `/mnt/media`.
- `error_not_found.json` — rclone's error answer for an unknown call (HTTP 404).
