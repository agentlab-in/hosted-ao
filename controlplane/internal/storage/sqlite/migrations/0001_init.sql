-- +goose Up
-- +goose StatementBegin

-- accounts holds one row per signed-in Google identity: the immutable
-- Google subject that uniquely identifies the person, plus the email shown
-- in the UI.
CREATE TABLE accounts (
    id             TEXT PRIMARY KEY,
    google_subject TEXT NOT NULL UNIQUE,
    email          TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL
);

-- machines holds one row per VM bound to an account via ao setup-vm. hostname
-- is the machine's public URL, used as the reverse-proxy target. The JWT
-- audience is machines.id, not this column: see TOKEN_CONTRACT.md, the issuer
-- (internal/tokens/access.go), and the gateway verifier, which all use the
-- machine id. revoked_at, once set, ends the machine's ability to verify
-- tokens against it.
CREATE TABLE machines (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES accounts (id),
    name        TEXT NOT NULL,
    hostname    TEXT NOT NULL,
    last_seen   TIMESTAMP,
    created_at  TIMESTAMP NOT NULL,
    revoked_at  TIMESTAMP
);
CREATE INDEX idx_machines_account ON machines (account_id);

-- device_codes backs the RFC 8628 device authorization flow used by
-- ao setup-vm. device_code holds the SHA-256 hash of the opaque, high-entropy
-- value the VM polls with, never that value itself: it is a bearer secret, so
-- a database leak must not hand over live device codes, and hashing also
-- makes the lookup a fixed-width comparison. user_code is the short value a
-- human types into the enter-code page, stored as typed because the approval
-- page displays it back. account_id is filled in once the human approves the
-- request in their browser.
CREATE TABLE device_codes (
    device_code    TEXT PRIMARY KEY,
    user_code      TEXT NOT NULL UNIQUE,
    account_id     TEXT REFERENCES accounts (id),
    status         TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    created_at     TIMESTAMP NOT NULL,
    expires_at     TIMESTAMP NOT NULL,
    last_polled_at TIMESTAMP,
    approved_at    TIMESTAMP
);

-- refresh_tokens stores only the hash of each issued refresh token, never
-- the token itself, so a database leak cannot be used to mint new access
-- tokens. Each row is bound to one account and one desktop install
-- (install_id), matching the token contract's rotation and revocation model.
CREATE TABLE refresh_tokens (
    id           TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL REFERENCES accounts (id),
    install_id   TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL,
    expires_at   TIMESTAMP,
    last_used_at TIMESTAMP,
    revoked_at   TIMESTAMP
);
CREATE INDEX idx_refresh_tokens_account ON refresh_tokens (account_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE refresh_tokens;
DROP TABLE device_codes;
DROP TABLE machines;
DROP TABLE accounts;
-- +goose StatementEnd
