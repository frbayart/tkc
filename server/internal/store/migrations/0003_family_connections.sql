-- Parent-to-parent connections established through invitations.
-- See protocol/family-connections.md.

-- The family that issued the invitation must confirm the connection after
-- the other family accepted it.
ALTER TABLE family_connections
    ADD COLUMN issuer_family uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    ADD COLUMN invitation_id uuid REFERENCES invitations (id) ON DELETE SET NULL,
    ADD COLUMN accepted_at   timestamptz NOT NULL,
    ADD COLUMN updated_at    timestamptz NOT NULL,
    ADD CONSTRAINT family_connections_issuer_check CHECK (issuer_family IN (family_a, family_b)),
    ADD CONSTRAINT family_connections_confirmed_check CHECK (status <> 'active' OR confirmed_at IS NOT NULL),
    ADD CONSTRAINT family_connections_revoked_check CHECK ((status = 'revoked') = (revoked_at IS NOT NULL));

ALTER TABLE family_connections ALTER COLUMN created_at DROP DEFAULT;

-- Invitations listed by their issuer.
CREATE INDEX invitations_issuer_idx ON invitations (issuer_family_id, created_at DESC);
