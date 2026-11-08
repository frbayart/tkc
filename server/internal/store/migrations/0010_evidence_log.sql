-- Evidence log and database-level protection of evidence (spec/evidence.md).

-- Append-only event log. Events are hash-chained per subject (a member, a
-- relationship, a family connection, a proposal), so that purging a subject
-- at the end of the evidence retention period removes a whole chain without
-- breaking the others.
CREATE TABLE evidence_events (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type     text NOT NULL CHECK (subject_type IN ('user', 'relationship', 'connection', 'proposal')),
    subject_id       uuid NOT NULL,
    seq              bigint NOT NULL CHECK (seq > 0),
    occurred_at      timestamptz NOT NULL,
    event_type       text NOT NULL,
    -- Actor, kept as plain values so that the record never changes.
    actor_user_id    uuid,
    actor_device_id  uuid,
    actor_profile_id uuid,
    -- Canonical JSON, exactly as hashed.
    data             text NOT NULL,
    prev_hash        bytea,
    hash             bytea NOT NULL,
    UNIQUE (subject_type, subject_id, seq),
    CHECK ((seq = 1) = (prev_hash IS NULL))
);

-- Each family's policy versions for a relationship. Private to the family:
-- the shared event log only records version numbers.
CREATE TABLE relationship_policy_versions (
    relationship_id uuid NOT NULL REFERENCES relationships (id) ON DELETE CASCADE,
    family_id       uuid NOT NULL,
    version         integer NOT NULL CHECK (version > 0),
    policy          jsonb NOT NULL,
    created_by      uuid,
    created_at      timestamptz NOT NULL,
    PRIMARY KEY (relationship_id, family_id, version)
);

-- Policy versions written before this migration.
INSERT INTO relationship_policy_versions (relationship_id, family_id, version, policy, created_by, created_at)
SELECT relationship_id, family_id, version, policy, updated_by, updated_at FROM relationship_policies;

-- The clock used by the protections below. The purge job sets tkc.clock for
-- its transaction; otherwise the database time is used.
CREATE FUNCTION tkc_clock() RETURNS timestamptz LANGUAGE sql STABLE AS $$
    SELECT coalesce(nullif(current_setting('tkc.clock', true), '')::timestamptz, now())
$$;

CREATE FUNCTION tkc_reject_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

-- Event log: never updated; deleted only once its subject has been purged.
CREATE FUNCTION evidence_events_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (OLD.subject_type = 'user' AND EXISTS (SELECT 1 FROM users WHERE id = OLD.subject_id))
        OR (OLD.subject_type = 'relationship' AND EXISTS (SELECT 1 FROM relationships WHERE id = OLD.subject_id))
        OR (OLD.subject_type = 'connection' AND EXISTS (SELECT 1 FROM family_connections WHERE id = OLD.subject_id))
        OR (OLD.subject_type = 'proposal' AND EXISTS (SELECT 1 FROM relationship_proposals WHERE id = OLD.subject_id)) THEN
        RAISE EXCEPTION 'evidence events are only deleted with their subject' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER evidence_events_no_update BEFORE UPDATE ON evidence_events
    FOR EACH ROW EXECUTE FUNCTION tkc_reject_update();
CREATE TRIGGER evidence_events_guard_delete BEFORE DELETE ON evidence_events
    FOR EACH ROW EXECUTE FUNCTION evidence_events_guard_delete();

-- Policy versions: never updated; deleted only with their relationship.
CREATE FUNCTION policy_versions_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM relationships WHERE id = OLD.relationship_id) THEN
        RAISE EXCEPTION 'policy versions are only deleted with their relationship' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER policy_versions_no_update BEFORE UPDATE ON relationship_policy_versions
    FOR EACH ROW EXECUTE FUNCTION tkc_reject_update();
CREATE TRIGGER policy_versions_guard_delete BEFORE DELETE ON relationship_policy_versions
    FOR EACH ROW EXECUTE FUNCTION policy_versions_guard_delete();

-- Accounts: deleted only by the purge, after the evidence retention period
-- and while no preservation hold is active on one of their relationships.
-- This also protects everything that cascades from an account.
CREATE FUNCTION users_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'removed' OR OLD.purge_after > tkc_clock() OR EXISTS (
        SELECT 1 FROM preservation_holds h JOIN relationships r ON r.id = h.relationship_id
        WHERE h.released_at IS NULL AND h.expires_at > tkc_clock() AND OLD.id IN (r.child_a, r.child_b)) THEN
        RAISE EXCEPTION 'accounts are only deleted by the evidence purge' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER users_guard_delete BEFORE DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION users_guard_delete();

-- Relationships: deleted only together with one of their children.
CREATE FUNCTION relationships_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT count(*) FROM users WHERE id IN (OLD.child_a, OLD.child_b)) = 2 THEN
        RAISE EXCEPTION 'relationships are only deleted with a purged child' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER relationships_guard_delete BEFORE DELETE ON relationships
    FOR EACH ROW EXECUTE FUNCTION relationships_guard_delete();

-- Messages: never updated; deleted only with their relationship or sender.
CREATE FUNCTION messages_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM relationships WHERE id = OLD.relationship_id)
        AND EXISTS (SELECT 1 FROM devices WHERE id = OLD.sender_device_id) THEN
        RAISE EXCEPTION 'messages are only deleted by the evidence purge' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER messages_no_update BEFORE UPDATE ON messages
    FOR EACH ROW EXECUTE FUNCTION tkc_reject_update();
CREATE TRIGGER messages_guard_delete BEFORE DELETE ON messages
    FOR EACH ROW EXECUTE FUNCTION messages_guard_delete();
