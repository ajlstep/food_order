-- Companies, groups and invitations.

-- "One company per person" and "one group per person" are UNIQUE here, in the
-- database, not only in the code. deleted_at: a closed company or group stays,
-- because old orders point to it.
CREATE TABLE companies (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    owner_id         TEXT NOT NULL REFERENCES users(id),
    max_delivery_fee INTEGER,                        -- bani; NULL = no limit, 0 = only free delivery
    deleted_at       INTEGER,
    created_at       INTEGER NOT NULL
);

CREATE TABLE company_members (
    company_id TEXT NOT NULL REFERENCES companies(id),
    user_id    TEXT NOT NULL UNIQUE REFERENCES users(id),
    joined_at  INTEGER NOT NULL,
    PRIMARY KEY (company_id, user_id)
);

CREATE TABLE groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    owner_id   TEXT NOT NULL REFERENCES users(id),
    company_id TEXT REFERENCES companies(id),        -- NULL = free group
    address    TEXT NOT NULL,
    lat        REAL,
    lon        REAL,
    notes      TEXT NOT NULL DEFAULT '',
    deleted_at INTEGER,
    created_at INTEGER NOT NULL
);

CREATE TABLE group_members (
    group_id  TEXT NOT NULL REFERENCES groups(id),
    user_id   TEXT NOT NULL UNIQUE REFERENCES users(id),
    joined_at INTEGER NOT NULL,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE invites (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('company', 'group')),
    target_id  TEXT NOT NULL,                        -- the company or the group
    from_user  TEXT NOT NULL REFERENCES users(id),
    to_user    TEXT NOT NULL REFERENCES users(id),
    status     TEXT NOT NULL DEFAULT 'open'
               CHECK (status IN ('open', 'accepted', 'declined', 'expired')),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX invites_to_user ON invites(to_user, status);
-- At most one open invitation to the same place for the same person.
CREATE UNIQUE INDEX invites_one_open ON invites(kind, target_id, to_user) WHERE status = 'open';
