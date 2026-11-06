-- Child relationships: lifecycle details and parent proposals.
-- See protocol/child-relationships.md.

ALTER TABLE relationships
    -- Family that suspended an active relationship; only it can resume it.
    ADD COLUMN suspended_by  uuid REFERENCES families (id) ON DELETE CASCADE,
    -- In-person exchange code or parent proposal that created the relationship.
    ADD COLUMN invitation_id uuid REFERENCES invitations (id) ON DELETE SET NULL,
    ADD CONSTRAINT relationships_suspended_check
        CHECK ((status = 'suspended') = (suspended_by IS NOT NULL)),
    ADD CONSTRAINT relationships_suspended_by_check
        CHECK (suspended_by IS NULL OR suspended_by IN (family_a, family_b)),
    -- Children of the same family are not linked through relationships.
    ADD CONSTRAINT relationships_distinct_families CHECK (family_a <> family_b),
    -- Status reflects the number of parental approvals.
    ADD CONSTRAINT relationships_proposed_check
        CHECK (status <> 'proposed' OR (approved_a_at IS NULL AND approved_b_at IS NULL)),
    ADD CONSTRAINT relationships_pending_check
        CHECK (status <> 'pending' OR ((approved_a_at IS NULL) <> (approved_b_at IS NULL)));

ALTER TABLE relationships ALTER COLUMN created_at DROP DEFAULT;
ALTER TABLE relationships ALTER COLUMN updated_at DROP DEFAULT;
ALTER TABLE relationship_policies ALTER COLUMN updated_at DROP DEFAULT;

-- A parent proposes one of their children to a connected family; a parent
-- of that family accepts by choosing one of their own children. Child IDs
-- are therefore never disclosed to the other family before both agree.
CREATE TABLE relationship_proposals (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id   uuid NOT NULL REFERENCES family_connections (id) ON DELETE CASCADE,
    proposer_family uuid NOT NULL,
    proposer_child  uuid NOT NULL,
    proposer_user   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    target_family   uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    -- The proposing family's policy, which becomes its relationship policy.
    proposer_policy jsonb NOT NULL,
    status          text NOT NULL CHECK (status IN ('pending', 'accepted', 'declined', 'cancelled')),
    relationship_id uuid REFERENCES relationships (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    FOREIGN KEY (proposer_child, proposer_family) REFERENCES users (id, family_id) ON DELETE CASCADE,
    CHECK (proposer_family <> target_family),
    CHECK (status <> 'accepted' OR relationship_id IS NOT NULL)
);

CREATE INDEX relationship_proposals_target_idx ON relationship_proposals (target_family) WHERE status = 'pending';
CREATE INDEX relationship_proposals_proposer_idx ON relationship_proposals (proposer_family) WHERE status = 'pending';
CREATE INDEX relationships_family_a_idx ON relationships (family_a);
CREATE INDEX relationships_family_b_idx ON relationships (family_b);
