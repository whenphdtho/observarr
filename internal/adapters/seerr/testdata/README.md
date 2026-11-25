# Fixtures

Every file here is hand-written from the apps' published API documentation (overseerr-api.yml and
seerr-api.yml). None of it was captured from a real running instance; the users and e-mail addresses are
made up.

- `requests.json` — `GET /api/v1/request`: seven requests in every state (pending, approved and
  processing, a 4K request whose 4K copy is processing, completed and available, partially available, declined, failed),
  one requester whose display name is an e-mail address, one request without a TMDB id and one with an
  unreadable creation time.
