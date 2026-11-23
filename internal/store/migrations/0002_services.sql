CREATE TABLE services (
    id         INTEGER PRIMARY KEY,
    type       TEXT    NOT NULL,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    base_url   TEXT    NOT NULL,
    api_key    BLOB,                 -- secret.Box sealed with context "service:<id>"; NULL = no key
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
