-- A person (account) and the channels they talk to us on.

CREATE TABLE users (
    id                 TEXT PRIMARY KEY,             -- UUIDv7, made by this service
    display_name       TEXT NOT NULL DEFAULT '',
    email              TEXT UNIQUE,                  -- only after confirmation, lower case
    email_confirmed_at INTEGER,
    phone              TEXT UNIQUE,
    language           TEXT NOT NULL DEFAULT 'ro',
    status             TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'active', 'blocked', 'deleted')),
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

-- One row per (channel, person in that channel). A new messenger is new rows,
-- not a new table.
CREATE TABLE identities (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT NOT NULL REFERENCES users(id),
    channel      TEXT NOT NULL,                      -- telegram
    external_id  TEXT NOT NULL,                      -- the person's id in the channel
    chat_ref     TEXT NOT NULL DEFAULT '',           -- where messages go (Telegram: chat id)
    nickname     TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    language     TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active', 'stopped', 'blocked')),
    last_seen_at INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (channel, external_id)
);
CREATE INDEX identities_user ON identities(user_id);
