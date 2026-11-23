CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,          -- '!' = no password login (trusted-proxy users)
    created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash BLOB    PRIMARY KEY,          -- SHA-256 of the cookie token; the token itself is never stored
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf       TEXT    NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expires_at);

CREATE TABLE audit (
    id     INTEGER PRIMARY KEY,
    ts     INTEGER NOT NULL,
    actor  TEXT    NOT NULL,
    action TEXT    NOT NULL,
    detail TEXT    NOT NULL DEFAULT ''
);
