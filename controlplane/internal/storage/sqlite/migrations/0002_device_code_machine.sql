-- +goose Up
-- +goose StatementBegin

-- The RFC 8628 device authorization request carries the machine it is about
-- to bind, because the approval page has to show the operator what they are
-- approving and the approval itself has to insert the machines row. Neither
-- value is known to the control plane by any other route: the VM is the only
-- party that knows its own public URL.
--
-- machine_id is filled in at approval and is what the polling client is told.
-- It is machines.id, which is also the `aud` of every access token minted for
-- this machine, never machine_public_url. See TOKEN_CONTRACT.md.
ALTER TABLE device_codes ADD COLUMN machine_name TEXT NOT NULL DEFAULT '';
ALTER TABLE device_codes ADD COLUMN machine_public_url TEXT NOT NULL DEFAULT '';
ALTER TABLE device_codes ADD COLUMN machine_id TEXT REFERENCES machines (id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE device_codes DROP COLUMN machine_id;
ALTER TABLE device_codes DROP COLUMN machine_public_url;
ALTER TABLE device_codes DROP COLUMN machine_name;
-- +goose StatementEnd
