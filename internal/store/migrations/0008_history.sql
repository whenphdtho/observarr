-- Traffic and host figures over time, in buckets of a minute (res 60), an hour (3600) or a local day (86400).
-- ts is the bucket's start in seconds since the epoch; a day bucket starts at local midnight. n counts the
-- minutes a row holds, so an hour or a day that was watched only in part still gives the right averages.
CREATE TABLE traffic_series (
    res    INTEGER NOT NULL,
    ts     INTEGER NOT NULL,
    source TEXT    NOT NULL, -- "host", "container:<name>", or "other": the containers beyond a bucket's 64 sources
    rx     INTEGER NOT NULL, -- bytes received in the bucket
    tx     INTEGER NOT NULL, -- bytes sent in the bucket
    n      INTEGER NOT NULL,
    PRIMARY KEY (res, ts, source)
) WITHOUT ROWID;

CREATE TABLE host_series (
    res INTEGER NOT NULL,
    ts  INTEGER NOT NULL,
    cpu REAL    NOT NULL, -- average CPU use, percent of all CPUs
    mem INTEGER NOT NULL, -- average memory used, bytes
    n   INTEGER NOT NULL,
    PRIMARY KEY (res, ts)
) WITHOUT ROWID;
