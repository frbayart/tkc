-- Versioned, immutable profiles (spec/evidence.md). A profile (display name,
-- avatar...) is never modified: every change creates a new version linked to
-- the previous one, and messages record the sender's version at sending time,
-- so evidence shows the name in use when each message was sent.

CREATE TABLE user_profiles (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    version          integer NOT NULL CHECK (version > 0),
    -- End-to-end encrypted profile content, opaque to the server.
    ciphertext       bytea NOT NULL,
    -- Ed25519 signature by the author device (protocol/profiles.md).
    signature        bytea NOT NULL,
    -- Author, kept as plain values: the record must survive the author's
    -- own purge unchanged.
    author_user_id   uuid NOT NULL,
    author_device_id uuid NOT NULL,
    created_at       timestamptz NOT NULL,
    -- Link to the previous version; the next version is the one pointing to
    -- this one, so the chain can be followed both ways.
    previous_id      uuid UNIQUE REFERENCES user_profiles (id) ON DELETE CASCADE,
    UNIQUE (user_id, version),
    CHECK ((version = 1) = (previous_id IS NULL))
);

ALTER TABLE users
    ADD COLUMN current_profile_id uuid REFERENCES user_profiles (id) ON DELETE SET NULL,
    DROP COLUMN profile_ciphertext;

ALTER TABLE messages
    ADD COLUMN sender_profile_id uuid REFERENCES user_profiles (id) ON DELETE CASCADE;

-- Profile versions are evidence: they can never be modified, and can only be
-- deleted together with their user, by the purge at the end of the evidence
-- retention period.
CREATE FUNCTION user_profiles_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'profile versions are immutable' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM users WHERE id = OLD.user_id) THEN
        RAISE EXCEPTION 'profile versions are only deleted with their user' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER user_profiles_immutable
    BEFORE UPDATE OR DELETE ON user_profiles
    FOR EACH ROW EXECUTE FUNCTION user_profiles_immutable();
