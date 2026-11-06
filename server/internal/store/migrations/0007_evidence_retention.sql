-- Removing a member no longer deletes data immediately. The account is
-- retired at once (hidden, devices revoked, relationships revoked), but its
-- data is kept as evidence for both families (legal proceedings,
-- harassment, psychological follow-up) until purge_after, then purged.
-- See spec/retention.md, "Evidence retention".

ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users
    ADD CONSTRAINT users_status_check CHECK (status IN ('active', 'disabled', 'removed')),
    ADD COLUMN removed_at  timestamptz,
    ADD COLUMN purge_after timestamptz,
    ADD CONSTRAINT users_removed_check
        CHECK ((status = 'removed') = (removed_at IS NOT NULL AND purge_after IS NOT NULL)),
    ADD CONSTRAINT users_purge_after_check CHECK (purge_after IS NULL OR purge_after > removed_at);

CREATE INDEX users_purge_idx ON users (purge_after) WHERE status = 'removed';

ALTER TABLE relationships DROP CONSTRAINT relationships_revocation_reason_check;
ALTER TABLE relationships ADD CONSTRAINT relationships_revocation_reason_check
    CHECK (revocation_reason IN ('parent', 'child_block', 'connection_revoked', 'member_removed'));
