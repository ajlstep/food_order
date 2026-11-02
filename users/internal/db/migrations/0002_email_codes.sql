-- Confirmation of the email address.

-- The code that is valid now. One per account: a new code replaces the old.
-- The code itself is not stored, only its HMAC.
CREATE TABLE email_codes (
    user_id    TEXT PRIMARY KEY REFERENCES users(id),
    email      TEXT NOT NULL,
    code_hash  TEXT NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,          -- wrong attempts so far
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- Every code that was sent, for the limits (pause between codes, codes per
-- hour). Rows older than a day are deleted.
CREATE TABLE email_code_sends (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    email   TEXT NOT NULL,
    sent_at INTEGER NOT NULL
);
CREATE INDEX email_code_sends_email ON email_code_sends(email, sent_at);
CREATE INDEX email_code_sends_user ON email_code_sends(user_id, sent_at);
