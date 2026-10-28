-- Replies, edits and "delete for everyone" tombstones.
ALTER TABLE messages ADD COLUMN reply_to BLOB;
ALTER TABLE messages ADD COLUMN edited_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0;

-- One reaction per person per message: mine = 1 is ours, 0 the contact's.
CREATE TABLE reactions (
    peer_id TEXT NOT NULL,
    msg_id BLOB NOT NULL,
    mine INTEGER NOT NULL,
    emoji TEXT NOT NULL,
    PRIMARY KEY (peer_id, msg_id, mine)
);

-- Changes still to send to a contact (edits, deletions, reactions, read
-- receipts), each a serialized pb.Update, sent in id (UUIDv7) order.
CREATE TABLE updates (
    peer_id TEXT NOT NULL,
    id BLOB NOT NULL,
    frame BLOB NOT NULL,
    PRIMARY KEY (peer_id, id)
);

-- Full-text search over message text, kept in step by triggers. Accents
-- are ignored, so "maine" finds "mâine".
CREATE VIRTUAL TABLE messages_fts USING fts5(
    text, content = 'messages', content_rowid = 'rowid',
    tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TRIGGER messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts (rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts (messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;
CREATE TRIGGER messages_fts_update AFTER UPDATE OF text ON messages BEGIN
    INSERT INTO messages_fts (messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
    INSERT INTO messages_fts (rowid, text) VALUES (new.rowid, new.text);
END;
INSERT INTO messages_fts (messages_fts) VALUES ('rebuild');
