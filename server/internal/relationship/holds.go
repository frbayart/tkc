package relationship

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/frbayart/tkc/server/internal/identity"
)

// DefaultHoldDuration is how long a preservation hold lasts unless renewed.
// Provisional value pending legal review.
const DefaultHoldDuration = 365 * 24 * time.Hour

// Hold is a family's preservation hold on a relationship.
type Hold struct {
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) holdDuration() time.Duration {
	if s.HoldDuration > 0 {
		return s.HoldDuration
	}
	return DefaultHoldDuration
}

// SetHold places, or renews, the caller's family preservation hold on a
// relationship, whatever its status. While a hold is active, the
// relationship's evidence is not deleted: call recordings are kept and the
// purge of a removed member involved in it is deferred.
func (s *Service) SetHold(ctx context.Context, p identity.Principal, id string) (Hold, error) {
	if err := requireParent(p); err != nil {
		return Hold{}, err
	}
	now := s.Now()
	var h Hold
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO preservation_holds
			(relationship_id, family_id, set_by_user, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (relationship_id, family_id) WHERE released_at IS NULL
			DO UPDATE SET expires_at = EXCLUDED.expires_at, set_by_user = EXCLUDED.set_by_user
			RETURNING created_at, expires_at`,
			x.id, p.FamilyID, p.UserID, now, now.Add(s.holdDuration())).Scan(&h.CreatedAt, &h.ExpiresAt)
	})
	if err != nil {
		return Hold{}, err
	}
	return h, nil
}

// ReleaseHold releases the caller's family hold. The other family's hold, if
// any, is unaffected.
func (s *Service) ReleaseHold(ctx context.Context, p identity.Principal, id string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		now := s.Now()
		tag, err := tx.Exec(ctx, `UPDATE preservation_holds SET released_at = $3
			WHERE relationship_id = $1 AND family_id = $2 AND released_at IS NULL AND expires_at > $3`,
			x.id, p.FamilyID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrNotFound
		}
		return nil
	})
}

// ourHold returns the caller's family active hold, if any.
func ourHold(ctx context.Context, tx pgx.Tx, relID, familyID string, now time.Time) (*Hold, error) {
	var h Hold
	err := tx.QueryRow(ctx, `SELECT created_at, expires_at FROM preservation_holds
		WHERE relationship_id = $1 AND family_id = $2 AND released_at IS NULL AND expires_at > $3`,
		relID, familyID, now).Scan(&h.CreatedAt, &h.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// HoldUntil returns the end of the latest active hold on a relationship, by
// any family, or the zero time if it is not held. It is meant for the
// recording retention job (see retention.ComputeWithHold).
func (s *Service) HoldUntil(ctx context.Context, relationshipID string) (time.Time, error) {
	if !validID(relationshipID) {
		return time.Time{}, nil
	}
	var until *time.Time
	err := s.pool.QueryRow(ctx, `SELECT max(expires_at) FROM preservation_holds
		WHERE relationship_id = $1 AND released_at IS NULL AND expires_at > $2`,
		relationshipID, s.Now()).Scan(&until)
	if err != nil || until == nil {
		return time.Time{}, err
	}
	return *until, nil
}
