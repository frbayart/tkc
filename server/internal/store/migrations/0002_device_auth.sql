-- Device enrollment and authentication. See protocol/device-auth.md.

-- Single-use, short-lived codes issued by a parent device so that a new
-- device can be enrolled for a member of the family. Only a hash of the code
-- is stored. The enrolled device stays `pending` until a parent device
-- approves it.
CREATE TABLE device_enrollments (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id           uuid NOT NULL,
    target_user_id      uuid NOT NULL,
    issued_by_device_id uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    code_hash           bytea NOT NULL UNIQUE,
    created_at          timestamptz NOT NULL,
    expires_at          timestamptz NOT NULL,
    consumed_at         timestamptz,
    device_id           uuid REFERENCES devices (id) ON DELETE SET NULL,
    FOREIGN KEY (target_user_id, family_id) REFERENCES users (id, family_id) ON DELETE CASCADE,
    CHECK (expires_at > created_at),
    CHECK ((consumed_at IS NULL) = (device_id IS NULL))
);

-- Single-use authentication challenges. Only a hash of the nonce is stored.
CREATE TABLE auth_challenges (
    nonce_hash bytea PRIMARY KEY,
    device_id  uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

CREATE INDEX auth_challenges_device_idx ON auth_challenges (device_id);

-- Bearer sessions. Only a hash of the token is stored.
CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    device_id  uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at)
);

CREATE INDEX sessions_device_idx ON sessions (device_id);
