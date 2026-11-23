# Fixtures

Every file here is hand-written from CrowdSec's public Local API documentation (the bouncer
endpoints in the LAPI Swagger specification). None of it was captured from, or contains any value
from, a real running instance: every address is from the documentation ranges 192.0.2.0/24,
198.51.100.0/24 and 203.0.113.0/24, and the UUIDs are made up.

- `decisions.json` — `GET /v1/decisions`: two community blocklist (`CAPI`) bans, one ban from a
  subscribed list (`lists`), four decisions of the local engine (`crowdsec`, one of them a captcha,
  one with an explicit `until`) and one manual range ban (`cscli`), not in id order.
- `forbidden.json` — the LAPI's answer to a missing or unknown bouncer key (sent with 403).
- `not_crowdsec.html` — a generic login page, standing in for something else answering on the
  configured address.
