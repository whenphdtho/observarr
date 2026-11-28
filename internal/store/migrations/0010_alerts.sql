-- Alerts: where they go (channels), what raises them (one rules document), what is firing now (state, so a
-- restart does not send everything again) and what was sent (log, with one delivery row per channel).
CREATE TABLE alert_channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused: it binds the sealed secrets
    type       TEXT    NOT NULL,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    settings   TEXT    NOT NULL,          -- JSON, nothing secret
    secrets    BLOB,                      -- secret.Box sealed JSON with context "alert-channel:<id>"; NULL = none
    rules      TEXT    NOT NULL,          -- JSON array of the rules this channel receives
    enabled    INTEGER NOT NULL DEFAULT 0,
    tested     INTEGER NOT NULL DEFAULT 0, -- a test got through since the settings or secrets last changed
    last_at    INTEGER NOT NULL DEFAULT 0, -- the latest delivery or test; 0 = none yet
    last_ok    INTEGER NOT NULL DEFAULT 0,
    last_note  TEXT    NOT NULL DEFAULT '', -- a short status line, never a response body
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE alert_rules (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    body TEXT    NOT NULL -- JSON
);

CREATE TABLE alert_state (
    key      TEXT    PRIMARY KEY,
    body     TEXT    NOT NULL, -- JSON: the condition as it was last seen
    since    INTEGER NOT NULL, -- firing since
    notified INTEGER NOT NULL  -- last sent; 0 = never
) WITHOUT ROWID;

CREATE TABLE alert_log (
    id       INTEGER PRIMARY KEY,
    at       INTEGER NOT NULL,
    event    TEXT    NOT NULL, -- firing, resolved, repeat, summary or test
    rule     TEXT    NOT NULL,
    severity TEXT    NOT NULL,
    service  TEXT    NOT NULL,
    title    TEXT    NOT NULL,
    message  TEXT    NOT NULL
);

CREATE TABLE alert_deliveries (
    log_id     INTEGER NOT NULL REFERENCES alert_log (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL, -- no reference: a removed channel's deliveries stay, under its name
    channel    TEXT    NOT NULL,
    status     TEXT    NOT NULL, -- queued, sent, failed, held, suppressed or dropped
    attempts   INTEGER NOT NULL,
    note       TEXT    NOT NULL,
    at         INTEGER NOT NULL,
    PRIMARY KEY (log_id, channel_id)
) WITHOUT ROWID;
