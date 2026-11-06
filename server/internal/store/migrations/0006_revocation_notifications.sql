-- Who revoked a relationship and why, and notifications for parents.
-- A child may block a relationship alone; their parents are notified so they
-- can talk about it with the child. See protocol/child-relationships.md.

ALTER TABLE relationships
    ADD COLUMN revoked_by_family uuid REFERENCES families (id) ON DELETE SET NULL,
    ADD COLUMN revocation_reason text
        CHECK (revocation_reason IN ('parent', 'child_block', 'connection_revoked')),
    ADD CONSTRAINT relationships_revoked_has_reason
        CHECK ((status = 'revoked') = (revocation_reason IS NOT NULL));

-- Notifications shown to the parents of a family. Push delivery is future
-- work; for now parents read them from the API.
CREATE TABLE family_notifications (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id       uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('relationship_blocked_by_child')),
    relationship_id uuid REFERENCES relationships (id) ON DELETE CASCADE,
    child_id        uuid REFERENCES users (id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL,
    read_at         timestamptz
);

CREATE INDEX family_notifications_family_idx ON family_notifications (family_id, created_at DESC);
