CREATE TABLE traffic_daily (
    day   TEXT    NOT NULL, -- local calendar day, YYYY-MM-DD
    scope TEXT    NOT NULL, -- "host", or "container:<name>"
    rx    INTEGER NOT NULL, -- bytes received that day
    tx    INTEGER NOT NULL, -- bytes sent that day
    PRIMARY KEY (day, scope)
) WITHOUT ROWID;
