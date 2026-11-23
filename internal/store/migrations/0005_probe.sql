CREATE TABLE probe (
    id INTEGER PRIMARY KEY CHECK (id = 1), -- a single row, rewritten to check that the database takes writes
    at INTEGER NOT NULL
);
