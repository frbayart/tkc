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
}

// New returns a service with default settings.
func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:          pool,
		Now:           time.Now,
		SessionTTL:    DefaultSessionTTL,
		ChallengeTTL:  DefaultChallengeTTL,
		EnrollmentTTL: DefaultEnrollmentTTL,
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
		return tx.QueryRow(ctx, `INSERT INTO devices
			(user_id, device_type, platform, identity_public_key, encryption_public_key,
			 status, registered_at, activated_at)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $6) RETURNING id`,
			p.UserID, keys.DeviceType, keys.Platform, keys.IdentityPublicKey, keys.EncryptionPublicKey, now,
		).Scan(&p.DeviceID)
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
	err := s.pool.QueryRow(ctx, `INSERT INTO users (family_id, role, time_zone, created_at)
		VALUES ($1, $2, $3, $4) RETURNING id`, p.FamilyID, role, timeZone, s.Now()).Scan(&id)
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
		_, err = tx.Exec(ctx, `UPDATE device_enrollments SET consumed_at = $2, device_id = $3 WHERE id = $1`,
			enrollmentID, now, deviceID)
		return err
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
		_, err = tx.Exec(ctx, `UPDATE devices SET status = 'active', activated_at = $2, approved_by_device_id = $3
			WHERE id = $1`, deviceID, s.Now(), p.DeviceID)
		return err
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
		if _, err := tx.Exec(ctx, `UPDATE devices SET status = 'revoked', revoked_at = $2 WHERE id = $1`,
			deviceID, s.Now()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE device_id = $1`, deviceID)
		return err
	})
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
	UserID            string   `json:"user_id"`
	Role              Role     `json:"role"`
	TimeZone          string   `json:"time_zone"`
	Status            string   `json:"status"`
	ProfileCiphertext []byte   `json:"profile_ciphertext"`
	Devices           []Device `json:"devices"`
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
	rows, err := s.pool.Query(ctx, `SELECT id, role, time_zone, status, profile_ciphertext
		FROM users WHERE family_id = $1 ORDER BY created_at, id`, p.FamilyID)
	if err != nil {
		return Family{}, err
	}
	members, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) {
		m := Member{Devices: []Device{}}
		return m, r.Scan(&m.UserID, &m.Role, &m.TimeZone, &m.Status, &m.ProfileCiphertext)
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
		WHERE u.family_id = $1 ORDER BY d.registered_at, d.id`, p.FamilyID)
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
	f.Members = members
	return f, nil
}
