// Package identity manages families, users, devices and device
// authentication (spec/identity-family.md, protocol/device-auth.md).
//
// The server stores only public keys. Devices authenticate by signing a
// single-use challenge with their Ed25519 identity key; no password exists.
// Every new device after a family's first one stays pending until an active
// parent device of the family approves it, after comparing its key.
package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/frbayart/tkc/server/internal/evidence"
)

// Role of a user in a family.
type Role string

// Roles.
const (
	RoleParent Role = "parent"
	RoleChild  Role = "child"
)

// Default lifetimes.
const (
	DefaultSessionTTL    = 7 * 24 * time.Hour
	DefaultChallengeTTL  = 2 * time.Minute
	DefaultEnrollmentTTL = 15 * time.Minute
)

// maxEncryptionKeySize bounds the opaque encryption public key; its format
// depends on the messaging protocol, which is not chosen yet.
const maxEncryptionKeySize = 1024

// DeviceKeys is what a device submits when it registers.
type DeviceKeys struct {
	DeviceType          string
	Platform            string
	IdentityPublicKey   []byte // Ed25519
	EncryptionPublicKey []byte // opaque
	// Proof is the Ed25519 signature of ProofMessage(EncryptionPublicKey)
	// by the identity key.
	Proof []byte
}

func (k DeviceKeys) validate() error {
	if k.DeviceType != "phone" && k.DeviceType != "tablet" {
		return fmt.Errorf("%w: device_type", ErrInvalid)
	}
	if k.Platform != "android" && k.Platform != "ios" {
		return fmt.Errorf("%w: platform", ErrInvalid)
	}
	if len(k.EncryptionPublicKey) == 0 || len(k.EncryptionPublicKey) > maxEncryptionKeySize {
		return fmt.Errorf("%w: encryption_public_key", ErrInvalid)
	}
	if !verify(k.IdentityPublicKey, ProofMessage(k.EncryptionPublicKey), k.Proof) {
		return fmt.Errorf("%w: invalid key proof", ErrInvalid)
	}
	return nil
}

// Principal is an authenticated device and its owner.
type Principal struct {
	DeviceID string
	UserID   string
	FamilyID string
	Role     Role
}

// Service implements identity operations on PostgreSQL.
type Service struct {
	pool *pgxpool.Pool
	// Now is the clock; replaceable in tests.
	Now           func() time.Time
	SessionTTL    time.Duration
	ChallengeTTL  time.Duration
	EnrollmentTTL time.Duration
	// EvidenceRetention is how long a removed member's data is kept.
	EvidenceRetention time.Duration
}

// New returns a service with default settings.
func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:          pool,
		Now:           time.Now,
		SessionTTL:    DefaultSessionTTL,
		ChallengeTTL:  DefaultChallengeTTL,
		EnrollmentTTL: DefaultEnrollmentTTL,

		EvidenceRetention: DefaultEvidenceRetention,
	}
}

func validTimeZone(tz string) error {
	if tz == "" {
		return fmt.Errorf("%w: time_zone", ErrInvalid)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("%w: time_zone", ErrInvalid)
	}
	return nil
}

func requireParent(p Principal) error {
	if p.Role != RoleParent {
		return ErrForbidden
	}
	return nil
}

// CreateFamily creates a family with its first parent and that parent's
// first device, which is active immediately: there is no one to approve it.
func (s *Service) CreateFamily(ctx context.Context, timeZone string, keys DeviceKeys) (Principal, error) {
	if err := validTimeZone(timeZone); err != nil {
		return Principal{}, err
	}
	if err := keys.validate(); err != nil {
		return Principal{}, err
	}
	now := s.Now()
	p := Principal{Role: RoleParent}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO families (created_at) VALUES ($1) RETURNING id`, now).Scan(&p.FamilyID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO users (family_id, role, time_zone, created_at)
			VALUES ($1, 'parent', $2, $3) RETURNING id`, p.FamilyID, timeZone, now).Scan(&p.UserID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO devices
			(user_id, device_type, platform, identity_public_key, encryption_public_key,
			 status, registered_at, activated_at)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $6) RETURNING id`,
			p.UserID, keys.DeviceType, keys.Platform, keys.IdentityPublicKey, keys.EncryptionPublicKey, now,
		).Scan(&p.DeviceID); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, evidence.Actor{UserID: p.UserID, DeviceID: p.DeviceID},
			userEvent(p.UserID, "member_added", map[string]any{"family_id": p.FamilyID, "role": "parent", "founder": true}),
			userEvent(p.UserID, "device_activated", deviceData(p.DeviceID, keys)))
	})
	if isUniqueViolation(err) {
		return Principal{}, fmt.Errorf("%w: identity key already registered", ErrConflict)
	}
	return p, err
}

// AddUser adds a child or an additional guardian to the caller's family.
func (s *Service) AddUser(ctx context.Context, p Principal, role Role, timeZone string) (string, error) {
	if err := requireParent(p); err != nil {
		return "", err
	}
	if role != RoleParent && role != RoleChild {
		return "", fmt.Errorf("%w: role", ErrInvalid)
	}
	if err := validTimeZone(timeZone); err != nil {
		return "", err
	}
	var id string
	now := s.Now()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO users (family_id, role, time_zone, created_at)
			VALUES ($1, $2, $3, $4) RETURNING id`, p.FamilyID, role, timeZone, now).Scan(&id); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p),
			userEvent(id, "member_added", map[string]any{"family_id": p.FamilyID, "role": string(role)}))
	})
	return id, err
}

// CreateEnrollment issues a single-use code allowing one new device to be
// enrolled for targetUserID, a member of the caller's family.
func (s *Service) CreateEnrollment(ctx context.Context, p Principal, targetUserID string) (string, time.Time, error) {
	if err := requireParent(p); err != nil {
		return "", time.Time{}, err
	}
	if !validID(targetUserID) {
		return "", time.Time{}, ErrNotFound
	}
	now := s.Now()
	expires := now.Add(s.EnrollmentTTL)
	code, hash := newSecret()
	tag, err := s.pool.Exec(ctx, `INSERT INTO device_enrollments
		(family_id, target_user_id, issued_by_device_id, code_hash, created_at, expires_at)
		SELECT u.family_id, u.id, $3, $4, $5, $6
		FROM users u WHERE u.id = $1 AND u.family_id = $2 AND u.status = 'active'`,
		targetUserID, p.FamilyID, p.DeviceID, hash, now, expires)
	if err != nil {
		return "", time.Time{}, err
	}
	if tag.RowsAffected() == 0 {
		return "", time.Time{}, ErrNotFound
	}
	return code, expires, nil
}

// Enroll redeems an enrollment code and registers a pending device.
// Invalid, expired and already used codes are indistinguishable.
func (s *Service) Enroll(ctx context.Context, code string, keys DeviceKeys) (deviceID, userID string, err error) {
	if err := keys.validate(); err != nil {
		return "", "", err
	}
	now := s.Now()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var enrollmentID string
		err := tx.QueryRow(ctx, `SELECT e.id, e.target_user_id FROM device_enrollments e
			JOIN users u ON u.id = e.target_user_id AND u.status = 'active'
			JOIN devices issuer ON issuer.id = e.issued_by_device_id AND issuer.status = 'active'
			WHERE e.code_hash = $1 AND e.consumed_at IS NULL AND e.expires_at > $2
			FOR UPDATE OF e`, hashSecret(code), now).Scan(&enrollmentID, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidCode
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO devices
			(user_id, device_type, platform, identity_public_key, encryption_public_key, status, registered_at)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6) RETURNING id`,
			userID, keys.DeviceType, keys.Platform, keys.IdentityPublicKey, keys.EncryptionPublicKey, now,
		).Scan(&deviceID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE device_enrollments SET consumed_at = $2, device_id = $3 WHERE id = $1`,
			enrollmentID, now, deviceID); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, evidence.Actor{DeviceID: deviceID},
			userEvent(userID, "device_enrolled", deviceData(deviceID, keys)))
	})
	if isUniqueViolation(err) {
		return "", "", fmt.Errorf("%w: identity key already registered", ErrConflict)
	}
	if err != nil {
		return "", "", err
	}
	return deviceID, userID, nil
}

// ApproveDevice activates a pending device of the caller's family. The
// approving parent passes the identity key it displayed and compared, so the
// server only activates the key the parent actually saw.
func (s *Service) ApproveDevice(ctx context.Context, p Principal, deviceID string, expectedIdentityKey []byte) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(deviceID) {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var key []byte
		err := tx.QueryRow(ctx, `SELECT d.identity_public_key FROM devices d
			JOIN users u ON u.id = d.user_id
			WHERE d.id = $1 AND u.family_id = $2 AND d.status = 'pending'
			FOR UPDATE OF d`, deviceID, p.FamilyID).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if string(key) != string(expectedIdentityKey) {
			return ErrKeyMismatch
		}
		var owner string
		if err := tx.QueryRow(ctx, `UPDATE devices SET status = 'active', activated_at = $2, approved_by_device_id = $3
			WHERE id = $1 RETURNING user_id`, deviceID, s.Now(), p.DeviceID).Scan(&owner); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, s.Now(), actorOf(p),
			userEvent(owner, "device_approved", map[string]any{"device_id": deviceID, "approved_by_device_id": p.DeviceID}))
	})
}

// RevokeDevice revokes a device of the caller's family. Parents may revoke
// any device of the family; other members only their own current device. The
// last active parent device cannot be revoked, since no key recovery process
// exists yet. All sessions of the revoked device are deleted.
func (s *Service) RevokeDevice(ctx context.Context, p Principal, deviceID string) error {
	if !validID(deviceID) {
		return ErrNotFound
	}
	if p.Role != RoleParent && deviceID != p.DeviceID {
		return ErrForbidden
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize revocations within the family.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM families WHERE id = $1 FOR UPDATE`, p.FamilyID); err != nil {
			return err
		}
		var role Role
		var status string
		err := tx.QueryRow(ctx, `SELECT u.role, d.status FROM devices d JOIN users u ON u.id = d.user_id
			WHERE d.id = $1 AND u.family_id = $2`, deviceID, p.FamilyID).Scan(&role, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == "revoked" {
			return nil
		}
		if role == RoleParent && status == "active" {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM devices d JOIN users u ON u.id = d.user_id
				WHERE u.family_id = $1 AND u.role = 'parent' AND u.status = 'active'
				AND d.status = 'active' AND d.id <> $2`, p.FamilyID, deviceID).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastParentDevice
			}
		}
		var owner string
		if err := tx.QueryRow(ctx, `UPDATE devices SET status = 'revoked', revoked_at = $2 WHERE id = $1
			RETURNING user_id`, deviceID, s.Now()).Scan(&owner); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE device_id = $1`, deviceID); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, s.Now(), actorOf(p),
			userEvent(owner, "device_revoked", map[string]any{"device_id": deviceID}))
	})
}

// RemoveUser retires a member of the caller's family. Within a family,
// members can always communicate; removing the account is how a member stops
// being visible and reachable. Only parents can remove members, and a removal
// that would leave the family without an active parent device is refused.
//
// The effect is immediate: the member disappears from the family, its
// devices are revoked, its sessions end, and its relationships with other
// families are revoked. Its data (relationships, messages, calls) is kept as
// evidence for both families until EvidenceRetention has elapsed, and is then
// deleted by PurgeRemovedMembers.
func (s *Service) RemoveUser(ctx context.Context, p Principal, userID string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(userID) {
		return ErrNotFound
	}
	now := s.Now()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize with revocations and other removals in the family.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM families WHERE id = $1 FOR UPDATE`, p.FamilyID); err != nil {
			return err
		}
		var role Role
		err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id = $1 AND family_id = $2 AND status <> 'removed'`,
			userID, p.FamilyID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if role == RoleParent {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM devices d JOIN users u ON u.id = d.user_id
				WHERE u.family_id = $1 AND u.role = 'parent' AND u.status = 'active'
				AND d.status = 'active' AND u.id <> $2`, p.FamilyID, userID).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastParentDevice
			}
		}
		steps := []struct {
			sql  string
			args []any
		}{
			{`UPDATE users SET status = 'removed', removed_at = $2, purge_after = $3 WHERE id = $1`,
				[]any{userID, now, now.Add(s.evidenceRetention())}},
			{`DELETE FROM sessions WHERE device_id IN (SELECT id FROM devices WHERE user_id = $1)`, []any{userID}},
			{`UPDATE devices SET status = 'revoked', revoked_at = $2 WHERE user_id = $1 AND status <> 'revoked'`,
				[]any{userID, now}},
			{`DELETE FROM device_enrollments WHERE target_user_id = $1 AND consumed_at IS NULL`, []any{userID}},
			{`UPDATE invitations SET revoked_at = $2
				WHERE (issuer_user_id = $1 OR child_id = $1) AND consumed_at IS NULL AND revoked_at IS NULL`,
				[]any{userID, now}},
		}
		for _, st := range steps {
			if _, err := tx.Exec(ctx, st.sql, st.args...); err != nil {
				return err
			}
		}
		events := []evidence.Entry{userEvent(userID, "member_removed",
			map[string]any{"purge_after": now.Add(s.evidenceRetention()).UTC().Format(time.RFC3339)})}
		rels, err := collectIDs(ctx, tx, `UPDATE relationships SET status = 'revoked', revoked_at = $2, updated_at = $2,
			suspended_by = NULL, revoked_by_family = $3, revocation_reason = 'member_removed'
			WHERE $1 IN (child_a, child_b) AND status <> 'revoked' RETURNING id`, userID, now, p.FamilyID)
		if err != nil {
			return err
		}
		for _, id := range rels {
			events = append(events, evidence.Entry{SubjectType: evidence.SubjectRelationship, SubjectID: id,
				Type: "relationship_revoked", Data: map[string]any{"reason": "member_removed", "by_family": p.FamilyID}})
		}
		props, err := collectIDs(ctx, tx, `UPDATE relationship_proposals SET status = 'cancelled', updated_at = $2
			WHERE (proposer_child = $1 OR proposer_user = $1) AND status = 'pending' RETURNING id`, userID, now)
		if err != nil {
			return err
		}
		for _, id := range props {
			events = append(events, evidence.Entry{SubjectType: evidence.SubjectProposal, SubjectID: id,
				Type: "proposal_cancelled", Data: map[string]any{"reason": "member_removed"}})
		}
		return evidence.Append(ctx, tx, now, actorOf(p), events...)
	})
}

// DefaultEvidenceRetention is how long a removed member's data is kept as
// evidence before being purged. Provisional value pending legal review.
const DefaultEvidenceRetention = 365 * 24 * time.Hour

func (s *Service) evidenceRetention() time.Duration {
	if s.EvidenceRetention > 0 {
		return s.EvidenceRetention
	}
	return DefaultEvidenceRetention
}

// PurgeRemovedMembers permanently deletes removed members whose evidence
// retention period has elapsed, with everything attached to them, unless a
// preservation hold is active on one of their relationships. It returns
// the number of members purged. It is meant to run periodically.
func (s *Service) PurgeRemovedMembers(ctx context.Context) (int64, error) {
	// Members involved in a relationship under an active preservation hold
	// are kept until the hold is released or expires.
	// The database guards (migration 0010) evaluate the same rules with the
	// clock given in tkc.clock.
	now := s.Now()
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('tkc.clock', $1, true)`, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM users u WHERE u.status = 'removed' AND u.purge_after <= $1
			AND NOT EXISTS (SELECT 1 FROM preservation_holds h JOIN relationships r ON r.id = h.relationship_id
				WHERE h.released_at IS NULL AND h.expires_at > $1 AND u.id IN (r.child_a, r.child_b))`, now)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		_, err = evidence.PurgeOrphans(ctx, tx)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Challenge returns a single-use nonce for deviceID to sign. To avoid
// revealing which devices exist, a nonce is returned for any well-formed ID;
// it is only stored, and thus usable, for active devices.
func (s *Service) Challenge(ctx context.Context, deviceID string) ([]byte, time.Time, error) {
	if !validID(deviceID) {
		return nil, time.Time{}, fmt.Errorf("%w: device_id", ErrInvalid)
	}
	now := s.Now()
	expires := now.Add(s.ChallengeTTL)
	nonce := randomBytes()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM auth_challenges WHERE device_id = $1 AND expires_at <= $2`, deviceID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_challenges (nonce_hash, device_id, expires_at)
			SELECT $1, d.id, $3 FROM devices d WHERE d.id = $2 AND d.status = 'active'`,
			hashBytes(nonce), deviceID, expires)
		return err
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	return nonce, expires, nil
}

// CreateSession verifies a signed challenge and opens a session. Every
// failure returns ErrUnauthenticated. The challenge is consumed even if the
// signature is wrong.
func (s *Service) CreateSession(ctx context.Context, deviceID string, nonce, signature []byte) (string, time.Time, error) {
	if !validID(deviceID) {
		return "", time.Time{}, ErrUnauthenticated
	}
	now := s.Now()
	expires := now.Add(s.SessionTTL)
	token, tokenHash := newSecret()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM auth_challenges WHERE nonce_hash = $1 AND device_id = $2 AND expires_at > $3`,
			hashBytes(nonce), deviceID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrUnauthenticated
		}
		var key []byte
		err = tx.QueryRow(ctx, `SELECT d.identity_public_key FROM devices d JOIN users u ON u.id = d.user_id
			WHERE d.id = $1 AND d.status = 'active' AND u.status = 'active'`, deviceID).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		msg, err := ChallengeMessage(deviceID, nonce)
		if err != nil || !verify(key, msg, signature) {
			return ErrUnauthenticated
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sessions (token_hash, device_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`,
			tokenHash, deviceID, now, expires); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE devices SET last_seen_at = $2 WHERE id = $1`, deviceID, now)
		return err
	})
	if errors.Is(err, ErrUnauthenticated) {
		// Commit the challenge consumption even though authentication failed.
		_, _ = s.pool.Exec(ctx, `DELETE FROM auth_challenges WHERE nonce_hash = $1`, hashBytes(nonce))
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Authenticate resolves a session token. Sessions of revoked devices or
// disabled users are rejected.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	err := s.pool.QueryRow(ctx, `SELECT d.id, u.id, u.family_id, u.role FROM sessions s
		JOIN devices d ON d.id = s.device_id
		JOIN users u ON u.id = d.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2 AND d.status = 'active' AND u.status = 'active'`,
		hashSecret(token), s.Now()).Scan(&p.DeviceID, &p.UserID, &p.FamilyID, &p.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	return p, err
}

// Logout deletes a session.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashSecret(token))
	return err
}

// Device is the public view of a device.
type Device struct {
	ID                  string     `json:"id"`
	DeviceType          string     `json:"device_type"`
	Platform            string     `json:"platform"`
	IdentityPublicKey   []byte     `json:"identity_public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key"`
	KeyVersion          int        `json:"key_version"`
	Status              string     `json:"status"`
	ApprovedByDeviceID  *string    `json:"approved_by_device_id"`
	RegisteredAt        time.Time  `json:"registered_at"`
	RevokedAt           *time.Time `json:"revoked_at"`
}

// Member is the public view of a family member.
type Member struct {
	UserID   string `json:"user_id"`
	Role     Role   `json:"role"`
	TimeZone string `json:"time_zone"`
	Status   string `json:"status"`
	// Profile is the member's current profile version, if any.
	Profile *Profile `json:"profile"`
	Devices []Device `json:"devices"`
}

// Family is the view of a family returned to its members. It includes every
// device and its public keys so that clients can verify device lists.
type Family struct {
	ID      string   `json:"family_id"`
	Members []Member `json:"members"`
}

// Family returns the caller's family.
func (s *Service) Family(ctx context.Context, p Principal) (Family, error) {
	f := Family{ID: p.FamilyID, Members: []Member{}}
	rows, err := s.pool.Query(ctx, `SELECT id, role, time_zone, status
		FROM users WHERE family_id = $1 AND status <> 'removed' ORDER BY created_at, id`, p.FamilyID)
	if err != nil {
		return Family{}, err
	}
	members, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) {
		m := Member{Devices: []Device{}}
		return m, r.Scan(&m.UserID, &m.Role, &m.TimeZone, &m.Status)
	})
	if err != nil {
		return Family{}, err
	}
	index := map[string]int{}
	for i, m := range members {
		index[m.UserID] = i
	}
	rows, err = s.pool.Query(ctx, `SELECT d.user_id, d.id, d.device_type, d.platform, d.identity_public_key,
		d.encryption_public_key, d.key_version, d.status, d.approved_by_device_id, d.registered_at, d.revoked_at
		FROM devices d JOIN users u ON u.id = d.user_id
		WHERE u.family_id = $1 AND u.status <> 'removed' ORDER BY d.registered_at, d.id`, p.FamilyID)
	if err != nil {
		return Family{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		var d Device
		if err := rows.Scan(&userID, &d.ID, &d.DeviceType, &d.Platform, &d.IdentityPublicKey,
			&d.EncryptionPublicKey, &d.KeyVersion, &d.Status, &d.ApprovedByDeviceID, &d.RegisteredAt, &d.RevokedAt); err != nil {
			return Family{}, err
		}
		members[index[userID]].Devices = append(members[index[userID]].Devices, d)
	}
	if err := rows.Err(); err != nil {
		return Family{}, err
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.UserID
	}
	profiles, err := CurrentProfiles(ctx, s.pool, ids)
	if err != nil {
		return Family{}, err
	}
	for i := range members {
		members[i].Profile = profiles[members[i].UserID]
	}
	f.Members = members
	return f, nil
}

func actorOf(p Principal) evidence.Actor {
	return evidence.Actor{UserID: p.UserID, DeviceID: p.DeviceID}
}

func userEvent(userID, typ string, data map[string]any) evidence.Entry {
	return evidence.Entry{SubjectType: evidence.SubjectUser, SubjectID: userID, Type: typ, Data: data}
}

func deviceData(deviceID string, k DeviceKeys) map[string]any {
	return map[string]any{
		"device_id": deviceID, "device_type": k.DeviceType, "platform": k.Platform,
		"identity_public_key": k.IdentityPublicKey,
	}
}

// collectIDs runs a statement returning one id column.
func collectIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
