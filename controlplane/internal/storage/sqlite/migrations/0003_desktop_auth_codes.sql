-- +goose Up
-- +goose StatementBegin

-- desktop_auth_codes backs the desktop app's authorization-code exchange
-- (docs/desktop-login-contract.md). One row is one code in flight, and it is
-- deleted the moment it is redeemed, in the same transaction that inserts the
-- refresh token it yields, so a replayed code finds nothing and mints nothing.
--
-- code_hash is the SHA-256 of the opaque code, never the code itself: it is a
-- bearer secret for the seconds between the loopback redirect and the token
-- request, so a database leak must not hand over live codes, and the lookup is
-- a fixed-width comparison. This mirrors device_codes.device_code.
--
-- redirect_uri and code_challenge are stored because the token endpoint has to
-- prove the caller is the same client that asked: the exchange re-checks the
-- redirect URI byte-for-byte and the PKCE verifier against this challenge, and
-- account_id is the identity the code is bound to. All three are fixed at
-- authorization time and none of them are taken from the token request.
CREATE TABLE desktop_auth_codes (
    code_hash      TEXT PRIMARY KEY,
    account_id     TEXT NOT NULL REFERENCES accounts (id),
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL,
    expires_at     TIMESTAMP NOT NULL
);
CREATE INDEX idx_desktop_auth_codes_expires ON desktop_auth_codes (expires_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE desktop_auth_codes;
-- +goose StatementEnd
