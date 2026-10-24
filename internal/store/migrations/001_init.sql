CREATE TABLE contacts (
    peer_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    nickname TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    pending_send INTEGER NOT NULL DEFAULT 0,
    addrs TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL
);

CREATE TABLE messages (
    peer_id TEXT NOT NULL,
    id BLOB NOT NULL,
    direction TEXT NOT NULL,
    text TEXT NOT NULL,
    sent_at INTEGER NOT NULL,
    received_at INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    read INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (peer_id, id)
);

CREATE INDEX messages_peer_sent ON messages (peer_id, sent_at, id);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
