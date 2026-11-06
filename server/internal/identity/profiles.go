package identity

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/frbayart/tkc/server/internal/evidence"
)

// MaxProfileSize bounds an encrypted profile. Profiles hold small fields
// (name, avatar reference); media is stored elsewhere.
const MaxProfileSize = 16 << 10

const profileDomain = "tkc/v1/profile\x00"

// ProfileMessage is the message the author device signs for a profile
// version. It binds the user, the version number and the previous version,
// so a version cannot be replayed for another user or moved in the chain.
// previousID is empty for version 1.
func ProfileMessage(userID string, version int, previousID string, ciphertext []byte) ([]byte, error) {
	uid, err := uuidBytes(userID)
	if err != nil {
		return nil, err
	}
	prev := make([]byte, 16)
	if previousID != "" {
		if prev, err = uuidBytes(previousID); err != nil {
			return nil, err
		}
	}
	msg := append([]byte(profileDomain), uid...)
	msg = binary.BigEndian.AppendUint32(msg, uint32(version))
	msg = append(msg, prev...)
	return append(msg, ciphertext...), nil
}

// Profile is one immutable version of a user's profile.
type Profile struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	Version        int       `json:"version"`
	Ciphertext     []byte    `json:"ciphertext"`
	Signature      []byte    `json:"signature"`
	AuthorUserID   string    `json:"author_user_id"`
	AuthorDeviceID string    `json:"author_device_id"`
	CreatedAt      time.Time `json:"created_at"`
	PreviousID     *string   `json:"previous_id"`
	NextID         *string   `json:"next_id"`
}

// ProfileColumns is the select list for ScanProfile.
const ProfileColumns = `p.id, p.user_id, p.version, p.ciphertext, p.signature, p.author_user_id,
	p.author_device_id, p.created_at, p.previous_id,
	(SELECT n.id FROM user_profiles n WHERE n.previous_id = p.id)`

// ScanProfile scans a row selected with ProfileColumns, on a user_profiles
// table aliased p.
func ScanProfile(r pgx.Row) (Profile, error) {
	var pr Profile
	err := r.Scan(&pr.ID, &pr.UserID, &pr.Version, &pr.Ciphertext, &pr.Signature, &pr.AuthorUserID,
		&pr.AuthorDeviceID, &pr.CreatedAt, &pr.PreviousID, &pr.NextID)
	return pr, err
}

// CreateProfile appends a new version to a user's profile. The author is the
// calling device: the user themself, or a parent of the user's family.
// previousID must be the user's current version (empty for the first one);
// otherwise ErrConflict is returned, so concurrent edits never silently
// overwrite each other. The signature must verify with the author device's
// identity key over ProfileMessage.
func (s *Service) CreateProfile(ctx context.Context, p Principal, userID, previousID string, ciphertext, signature []byte) (Profile, error) {
	if !validID(userID) || (previousID != "" && !validID(previousID)) {
		return Profile{}, ErrNotFound
	}
	if len(ciphertext) == 0 || len(ciphertext) > MaxProfileSize {
		return Profile{}, fmt.Errorf("%w: ciphertext size", ErrInvalid)
	}
	if userID != p.UserID && p.Role != RoleParent {
		return Profile{}, ErrForbidden
	}
	now := s.Now()
	var pr Profile
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current *string
		err := tx.QueryRow(ctx, `SELECT current_profile_id FROM users
			WHERE id = $1 AND family_id = $2 AND status = 'active' FOR UPDATE`, userID, p.FamilyID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		version := 1
		if current != nil {
			if previousID != *current {
				return fmt.Errorf("%w: profile changed since version %s", ErrConflict, previousID)
			}
			if err := tx.QueryRow(ctx, `SELECT version + 1 FROM user_profiles WHERE id = $1`, *current).Scan(&version); err != nil {
				return err
			}
		} else if previousID != "" {
			return fmt.Errorf("%w: no previous version", ErrConflict)
		}

		var key []byte
		if err := tx.QueryRow(ctx, `SELECT identity_public_key FROM devices WHERE id = $1 AND status = 'active'`,
			p.DeviceID).Scan(&key); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnauthenticated
			}
			return err
		}
		msg, err := ProfileMessage(userID, version, previousID, ciphertext)
		if err != nil || !verify(key, msg, signature) {
			return fmt.Errorf("%w: invalid profile signature", ErrInvalid)
		}

		var prev *string
		if previousID != "" {
			prev = &previousID
		}
		pr, err = ScanProfile(tx.QueryRow(ctx, `WITH p AS (
				INSERT INTO user_profiles (user_id, version, ciphertext, signature, author_user_id,
					author_device_id, created_at, previous_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *)
			SELECT `+ProfileColumns+` FROM p`,
			userID, version, ciphertext, signature, p.UserID, p.DeviceID, now, prev))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE users SET current_profile_id = $2 WHERE id = $1`, userID, pr.ID); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p), userEvent(userID, "profile_version_created",
			map[string]any{"profile_id": pr.ID, "version": pr.Version}))
	})
	if isUniqueViolation(err) {
		return Profile{}, fmt.Errorf("%w: concurrent profile change", ErrConflict)
	}
	return pr, err
}

// ProfileHistory returns all versions of a user's profile, oldest first. It
// is part of the evidence view, for parents only: parents of the user's own
// family, and parents of any family whose child has, or had, a relationship
// with the user, whatever its status and even after the user was removed.
func (s *Service) ProfileHistory(ctx context.Context, p Principal, userID string) ([]Profile, error) {
	if err := requireParent(p); err != nil {
		return nil, err
	}
	if !validID(userID) {
		return nil, ErrNotFound
	}
	var visible bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND family_id = $2)
		OR EXISTS (SELECT 1 FROM relationships
			WHERE (child_a = $1 AND family_b = $2) OR (child_b = $1 AND family_a = $2))`,
		userID, p.FamilyID).Scan(&visible)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT `+ProfileColumns+` FROM user_profiles p
		WHERE p.user_id = $1 ORDER BY p.version`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Profile, error) { return ScanProfile(r) })
}

// Querier is satisfied by a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// CurrentProfiles returns the current profile version of each given user
// that has one, keyed by user ID.
func CurrentProfiles(ctx context.Context, q Querier, userIDs []string) (map[string]*Profile, error) {
	out := map[string]*Profile{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT `+ProfileColumns+` FROM users u
		JOIN user_profiles p ON p.id = u.current_profile_id WHERE u.id = ANY($1::uuid[])`, userIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Profile, error) { return ScanProfile(r) })
	if err != nil {
		return nil, err
	}
	for i := range profiles {
		out[profiles[i].UserID] = &profiles[i]
	}
	return out, nil
}

// UserEvents returns a member's event timeline (membership, devices,
// profile versions) for the parents of the member's family, including after
// the member was removed.
func (s *Service) UserEvents(ctx context.Context, p Principal, userID string) (evidence.TimelineView, error) {
	if err := requireParent(p); err != nil {
		return evidence.TimelineView{}, err
	}
	if !validID(userID) {
		return evidence.TimelineView{}, ErrNotFound
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND family_id = $2)`,
		userID, p.FamilyID).Scan(&exists); err != nil {
		return evidence.TimelineView{}, err
	}
	if !exists {
		return evidence.TimelineView{}, ErrNotFound
	}
	return evidence.View(ctx, s.pool, evidence.SubjectUser, userID)
}
