-- Service IDs are never reused: a new service, one restored in place of the removed ones included, must not get the
-- ID of an earlier one, which sealed that one's key and named it in the live state. AUTOINCREMENT needs the table
-- rebuilt; every row keeps its ID, so the sealed keys stay bound to theirs. No other table refers to services.id.
CREATE TABLE services_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT    NOT NULL,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    base_url   TEXT    NOT NULL,
    api_key    BLOB,                 -- secret.Box sealed with context "service:<id>"; NULL = no key
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    container  TEXT    NOT NULL DEFAULT '' -- linked Docker container name; '' = none
);
INSERT INTO services_new (id, type, name, base_url, api_key, created_at, updated_at, container)
    SELECT id, type, name, base_url, api_key, created_at, updated_at, container FROM services;
DROP TABLE services;
ALTER TABLE services_new RENAME TO services;
