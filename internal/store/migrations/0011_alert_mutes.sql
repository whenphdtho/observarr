-- Mutes: an alert the user silenced for a while, by the key of its condition. A muted condition sends nothing
-- and is not counted until its mute ends, the condition is over, or it gets worse.
CREATE TABLE alert_mutes (
    key         TEXT    PRIMARY KEY,
    until       INTEGER NOT NULL, -- the mute ends then
    severity    TEXT    NOT NULL, -- the condition's severity when it was muted: a worse one breaks the mute
    fingerprint TEXT    NOT NULL, -- what the condition was when it was muted: another problem under the key breaks it
    created_by  TEXT    NOT NULL  -- the user who set it
) WITHOUT ROWID;
