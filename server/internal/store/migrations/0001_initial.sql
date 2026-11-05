-- Initial schema. See docs/data-model.md for the rationale of every column
-- and the metadata-minimization rules.
--
-- Rules:
--   * identifiers are random UUIDs; nothing is publicly enumerable;
--   * the server stores no private keys and no plaintext content;
--   * user-facing profile data (names, avatars) is encrypted client-side
--     and stored as an opaque blob.

-- Families and members -------------------------------------------------------

CREATE TABLE families (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id          uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    role               text NOT NULL CHECK (role IN ('parent', 'child')),
    -- IANA time zone; used to evaluate the family's schedules for a child.
    time_zone          text NOT NULL DEFAULT 'UTC',
    -- Client-side encrypted profile (display name, avatar...), opaque here.
    profile_ciphertext bytea,
    status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    -- Lets composite foreign keys assert that a user belongs to a family.
    UNIQUE (id, family_id)
);

CREATE INDEX users_family_idx ON users (family_id);

-- Devices --------------------------------------------------------------------

CREATE TABLE devices (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_type            text NOT NULL CHECK (device_type IN ('phone', 'tablet')),
    platform               text NOT NULL CHECK (platform IN ('android', 'ios')),
    -- Public keys only. Private keys never leave the device.
    identity_public_key    bytea NOT NULL,
    encryption_public_key  bytea NOT NULL,
    key_version            integer NOT NULL DEFAULT 1 CHECK (key_version > 0),
    status                 text NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'active', 'revoked')),
    -- Parent devices must be approved by an existing parent device (or a
    -- recovery process); a password alone never activates one.
    approved_by_device_id  uuid REFERENCES devices (id),
    registered_at          timestamptz NOT NULL DEFAULT now(),
    activated_at           timestamptz,
    last_seen_at           timestamptz,
    revoked_at             timestamptz,
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
    CHECK (status = 'pending' OR activated_at IS NOT NULL OR status = 'revoked')
);

CREATE INDEX devices_user_idx ON devices (user_id);
CREATE UNIQUE INDEX devices_identity_key_idx ON devices (identity_public_key);

-- Family connections (parent-to-parent trust) --------------------------------

CREATE TABLE family_connections (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Canonical ordering: family_a < family_b, one row per pair.
    family_a     uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    family_b     uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    status       text NOT NULL CHECK (status IN ('pending', 'active', 'revoked')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz,
    revoked_at   timestamptz,
    CHECK (family_a < family_b),
    UNIQUE (family_a, family_b)
);

-- Invitations ----------------------------------------------------------------

CREATE TABLE invitations (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind               text NOT NULL CHECK (kind IN ('family_connection', 'child_exchange')),
    issuer_family_id   uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    issuer_user_id     uuid NOT NULL,
    -- Set for in-person child exchanges: the child who displayed the code.
    child_id           uuid,
    -- Only a hash of the high-entropy token is stored.
    token_hash         bytea NOT NULL UNIQUE,
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    consumed_at        timestamptz,
    consumed_by_family uuid REFERENCES families (id) ON DELETE SET NULL,
    revoked_at         timestamptz,
    FOREIGN KEY (issuer_user_id, issuer_family_id) REFERENCES users (id, family_id) ON DELETE CASCADE,
    FOREIGN KEY (child_id, issuer_family_id) REFERENCES users (id, family_id) ON DELETE CASCADE,
    CHECK (expires_at > created_at),
    CHECK ((kind = 'child_exchange') = (child_id IS NOT NULL)),
    CHECK (consumed_at IS NULL OR revoked_at IS NULL)
);

CREATE INDEX invitations_expiry_idx ON invitations (expires_at)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

-- Child relationships --------------------------------------------------------

CREATE TABLE relationships (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Canonical ordering: child_a < child_b.
    child_a           uuid NOT NULL,
    family_a          uuid NOT NULL,
    child_b           uuid NOT NULL,
    family_b          uuid NOT NULL,
    status            text NOT NULL DEFAULT 'proposed'
                      CHECK (status IN ('proposed', 'pending', 'active', 'suspended', 'revoked')),
    origin            text NOT NULL CHECK (origin IN ('in_person', 'parent')),
    approved_a_at     timestamptz,
    approved_b_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    revoked_at        timestamptz,
    FOREIGN KEY (child_a, family_a) REFERENCES users (id, family_id) ON DELETE CASCADE,
    FOREIGN KEY (child_b, family_b) REFERENCES users (id, family_id) ON DELETE CASCADE,
    CHECK (child_a < child_b),
    -- A relationship can only be active once both families approved it.
    CHECK (status NOT IN ('active', 'suspended')
           OR (approved_a_at IS NOT NULL AND approved_b_at IS NOT NULL)),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

-- At most one non-revoked relationship per pair of children.
CREATE UNIQUE INDEX relationships_pair_idx ON relationships (child_a, child_b)
    WHERE status <> 'revoked';
CREATE INDEX relationships_child_b_idx ON relationships (child_b);

-- Each family's policy for a relationship (see internal/policy).
CREATE TABLE relationship_policies (
    relationship_id uuid NOT NULL REFERENCES relationships (id) ON DELETE CASCADE,
    family_id       uuid NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    policy          jsonb NOT NULL,
    version         integer NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (relationship_id, family_id)
);

-- Messages -------------------------------------------------------------------

CREATE TABLE messages (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    relationship_id  uuid NOT NULL REFERENCES relationships (id) ON DELETE CASCADE,
    sender_device_id uuid NOT NULL REFERENCES devices (id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- Opaque end-to-end encrypted envelope (content type, body, attachment
    -- references are all inside the ciphertext).
    ciphertext       bytea NOT NULL,
    -- Protocol-level metadata needed for delivery (e.g. epoch), never content.
    crypto_metadata  bytea
);

CREATE INDEX messages_relationship_idx ON messages (relationship_id, created_at, id);

-- Calls and recordings -------------------------------------------------------

CREATE TABLE calls (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    relationship_id    uuid NOT NULL REFERENCES relationships (id) ON DELETE CASCADE,
    caller_user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    call_type          text NOT NULL CHECK (call_type IN ('audio', 'video')),
    started_at         timestamptz NOT NULL DEFAULT now(),
    -- Authorized maximum end, computed by the policy engine at call start.
    authorized_until   timestamptz NOT NULL,
    ended_at           timestamptz,
    termination_reason text CHECK (termination_reason IN
        ('hangup', 'declined', 'missed', 'max_duration', 'schedule_end',
         'allowance_exhausted', 'relationship_revoked', 'error')),
    CHECK (authorized_until > started_at),
    CHECK (ended_at IS NULL OR ended_at >= started_at),
    CHECK ((ended_at IS NULL) = (termination_reason IS NULL))
);

CREATE INDEX calls_relationship_idx ON calls (relationship_id, started_at DESC);

CREATE TABLE recordings (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id         uuid NOT NULL UNIQUE REFERENCES calls (id) ON DELETE CASCADE,
    -- Location of the opaque encrypted object in object storage.
    object_key      text NOT NULL UNIQUE,
    size_bytes      bigint NOT NULL CHECK (size_bytes >= 0),
    started_at      timestamptz NOT NULL,
    -- Latest deletion time from the retention engine (internal/retention).
    expires_at      timestamptz NOT NULL,
    deleted_at      timestamptz,
    deletion_reason text CHECK (deletion_reason IN
        ('recording_disabled', 'count_exceeded', 'expired', 'parent_request')),
    CHECK ((deleted_at IS NULL) = (deletion_reason IS NULL))
);

CREATE INDEX recordings_live_expiry_idx ON recordings (expires_at) WHERE deleted_at IS NULL;
