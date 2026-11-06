-- Preservation holds: a parent can freeze the deletion of a relationship's
-- evidence (call recordings, and the purge of a removed member involved in
-- it) while a concern is being investigated. See spec/retention.md.

CREATE TABLE preservation_holds (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    relationship_id uuid NOT NULL REFERENCES relationships (id) ON DELETE CASCADE,
    -- Each family manages its own hold; only it can release it.
    family_id       uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    set_by_user     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL,
    -- Holds are bounded in time and renewable, so that none lasts forever
    -- by oversight.
    expires_at      timestamptz NOT NULL,
    released_at     timestamptz,
    CHECK (expires_at > created_at),
    CHECK (released_at IS NULL OR released_at >= created_at)
);

-- At most one unreleased hold per family and relationship.
CREATE UNIQUE INDEX preservation_holds_live_idx ON preservation_holds (relationship_id, family_id)
    WHERE released_at IS NULL;
