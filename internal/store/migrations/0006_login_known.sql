CREATE TABLE login_known (
    key TEXT    PRIMARY KEY, -- a client address (an IPv6 /64) that logged in successfully
    at  INTEGER NOT NULL     -- when it last did
);
