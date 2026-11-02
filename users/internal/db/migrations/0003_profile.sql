-- What an active person has set about themselves.

-- Key and value, so a new setting is not a new migration. Which keys exist,
-- their allowed values and defaults are in internal/profile/settings.go.
CREATE TABLE user_settings (
    user_id TEXT NOT NULL REFERENCES users(id),
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (user_id, key)
);

-- Allergens: always a hard filter, so they have their own table (a safety
-- matter, not a taste). The label is a lower case word as the catalog writes it.
CREATE TABLE allergens (
    user_id TEXT NOT NULL REFERENCES users(id),
    label   TEXT NOT NULL,
    PRIMARY KEY (user_id, label)
);

-- Tastes and goals. The order of the rows (id) is the order the person made.
CREATE TABLE preferences (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL REFERENCES users(id),
    kind    TEXT NOT NULL CHECK (kind IN ('like', 'dislike', 'diet', 'target')),
    value   TEXT NOT NULL,
    hard    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (user_id, kind, value)
);

-- Saved addresses.
CREATE TABLE locations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    TEXT NOT NULL REFERENCES users(id),
    label      TEXT NOT NULL,
    address    TEXT NOT NULL,
    lat        REAL,
    lon        REAL,
    notes      TEXT NOT NULL DEFAULT '',
    is_default INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX locations_user ON locations(user_id);
-- At most one default address per person, enforced by the database.
CREATE UNIQUE INDEX locations_one_default ON locations(user_id) WHERE is_default = 1;
