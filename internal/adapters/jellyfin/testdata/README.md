# Fixtures

Every file here is hand-written from Jellyfin's API types (MediaBrowser.Model: System/PublicSystemInfo.cs,
System/SystemInfo.cs, Dto/SessionInfoDto.cs, Session/PlayerStateInfo.cs, Session/TranscodingInfo.cs). None of it
was captured from a running server: names, addresses (documentation ranges only) and figures are invented, and the
titles are openly licensed films.

- `public_info.json` — `GET /System/Info/Public`.
- `system_info.json` — `GET /System/Info`, with a restart pending.
- `sessions.json` — `GET /Sessions`: a direct play, a transcode with its reasons, a paused episode whose audio
  alone is converted (a direct stream), and an idle session playing nothing.
