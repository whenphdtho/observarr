# Fixtures

Every file in this directory is hand-written from the publicly documented *arr API shapes
(https://wiki.servarr.com and each app's own Swagger UI). None of it was captured from, or
contains any value from, a real running instance: hostnames, API keys, download titles, and
other identifiers below are all invented for the test.

- `sonarr_status.json` — `GET /api/v3/system/status` for a Sonarr v4 instance.
- `sonarr_status_v2.json` — the same endpoint reporting an old, unsupported v2 instance.
- `sonarr_health.json` — `GET /api/v3/health` with one warning and one error check.
- `sonarr_queue.json` — `GET /api/v3/queue` with one downloading item, one with a warning
  status message, and one with a zero size.
- `prowlarr_status.json` — `GET /api/v1/system/status` for a Prowlarr v1 instance.
- `not_arr.html` — a generic HTML login page, standing in for something that isn't an *arr app
  at all answering on the configured address.
