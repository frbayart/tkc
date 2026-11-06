// Package relationship manages trust between families and relationships
// between children (spec/relationships.md, protocol/family-connections.md).
package relationship

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/frbayart/tkc/server/internal/identity"
)

// DefaultInvitationTTL is how long a family invitation can be accepted. It
// is sent over an external channel (SMS, e-mail...), so it must survive a
// delay, but stay short-lived.
const DefaultInvitationTTL = 48 * time.Hour

const codeSize = 32

// Service implements family connections and child relationships.
type Service struct {
	pool          *pgxpool.Pool
	Now           func() time.Time
	InvitationTTL time.Duration
	ExchangeTTL   time.Duration
	HoldDuration  time.Duration
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// New returns a service with default settings.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, Now: time.Now, InvitationTTL: DefaultInvitationTTL, ExchangeTTL: DefaultExchangeTTL, HoldDuration: DefaultHoldDuration}
}

func requireParent(p identity.Principal) error {
	if p.Role != identity.RoleParent {
		return identity.ErrForbidden
	}
	return nil
}

func newCode() (string, []byte) {
	b := make([]byte, codeSize)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("relationship: crypto/rand failed: %v", err))
	}
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(b), h[:]
}

// hashCode hashes a transported code; malformed input yields a hash that
// matches nothing.
func hashCode(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != codeSize {
		b = []byte("invalid:" + s)
	}
	h := sha256.Sum256(b)
	return h[:]
}

func validID(s string) bool { return identity.ValidID(s) }

// Invitation is a family invitation as seen by its issuer.
type Invitation struct {
	ID        string    `json:"id"`
	Code      string    `json:"code,omitempty"` // only returned at creation
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateInvitation issues a single-use invitation that another family's
// parent can accept to start a connection with the caller's family.
func (s *Service) CreateInvitation(ctx context.Context, p identity.Principal) (Invitation, error) {
	if err := requireParent(p); err != nil {
		return Invitation{}, err
	}
	now := s.Now()
	inv := Invitation{CreatedAt: now, ExpiresAt: now.Add(s.InvitationTTL)}
	code, hash := newCode()
	err := s.pool.QueryRow(ctx, `INSERT INTO invitations
		(kind, issuer_family_id, issuer_user_id, token_hash, created_at, expires_at)
		VALUES ('family_connection', $1, $2, $3, $4, $5) RETURNING id`,
		p.FamilyID, p.UserID, hash, now, inv.ExpiresAt).Scan(&inv.ID)
	if err != nil {
		return Invitation{}, err
	}
	inv.Code = code
	return inv, nil
}

// ListInvitations returns the caller's family invitations that can still
// be accepted.
func (s *Service) ListInvitations(ctx context.Context, p identity.Principal) ([]Invitation, error) {
	if err := requireParent(p); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, created_at, expires_at FROM invitations
		WHERE issuer_family_id = $1 AND kind = 'family_connection'
		AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id`, p.FamilyID, s.Now())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invitation, error) {
		var i Invitation
		return i, r.Scan(&i.ID, &i.CreatedAt, &i.ExpiresAt)
	})
}

// RevokeInvitation cancels an unused invitation of the caller's family.
func (s *Service) RevokeInvitation(ctx context.Context, p identity.Principal, id string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(id) {
		return identity.ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE invitations SET revoked_at = $3
		WHERE id = $1 AND issuer_family_id = $2 AND kind = 'family_connection'
		AND consumed_at IS NULL AND revoked_at IS NULL`, id, p.FamilyID, s.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrNotFound
	}
	return nil
}

// Connection is a family connection as seen by one of its families.
type Connection struct {
	ID            string `json:"id"`
	OtherFamilyID string `json:"other_family_id"`
	Status        string `json:"status"`
	// AwaitingUs is true when the connection is pending the caller's
	// family's confirmation.
	AwaitingUs  bool       `json:"awaiting_our_confirmation"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

// AcceptInvitation redeems another family's invitation. It creates a
// pending connection that the issuing family must confirm. Invalid, expired,
// used and revoked codes are indistinguishable.
func (s *Service) AcceptInvitation(ctx context.Context, p identity.Principal, code string) (Connection, error) {
	if err := requireParent(p); err != nil {
		return Connection{}, err
	}
	now := s.Now()
	var c Connection
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var invID, issuer string
		err := tx.QueryRow(ctx, `SELECT i.id, i.issuer_family_id FROM invitations i
			JOIN users u ON u.id = i.issuer_user_id AND u.status = 'active'
			WHERE i.token_hash = $1 AND i.kind = 'family_connection'
			AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > $2
			FOR UPDATE OF i`, hashCode(code), now).Scan(&invID, &issuer)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrInvalidCode
		}
		if err != nil {
			return err
		}
		if issuer == p.FamilyID {
			return fmt.Errorf("%w: own invitation", identity.ErrInvalid)
		}
		a, b := issuer, p.FamilyID
		if b < a {
			a, b = b, a
		}

		// Lock any existing connection for this pair.
		var existingID, status string
		err = tx.QueryRow(ctx, `SELECT id, status FROM family_connections
			WHERE family_a = $1 AND family_b = $2 FOR UPDATE`, a, b).Scan(&existingID, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `INSERT INTO family_connections
				(family_a, family_b, status, issuer_family, invitation_id, created_at, accepted_at, updated_at)
				VALUES ($1, $2, 'pending', $3, $4, $5, $5, $5) RETURNING id`,
				a, b, issuer, invID, now).Scan(&c.ID)
		case err != nil:
			return err
		case status != "revoked":
			return fmt.Errorf("%w: families already connected or pending", identity.ErrConflict)
		default:
			// Reconnection after a revocation starts a new confirmation round.
			c.ID = existingID
			_, err = tx.Exec(ctx, `UPDATE family_connections SET status = 'pending', issuer_family = $2,
				invitation_id = $3, created_at = $4, accepted_at = $4, updated_at = $4,
				confirmed_at = NULL, revoked_at = NULL WHERE id = $1`, existingID, issuer, invID, now)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE invitations SET consumed_at = $2, consumed_by_family = $3 WHERE id = $1`,
			invID, now, p.FamilyID)
		c.OtherFamilyID, c.Status, c.CreatedAt = issuer, "pending", now
		return err
	})
	if err != nil {
		return Connection{}, err
	}
	return c, nil
}

// ListConnections returns the connections of the caller's family.
func (s *Service) ListConnections(ctx context.Context, p identity.Principal) ([]Connection, error) {
	if err := requireParent(p); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,
			CASE WHEN family_a = $1 THEN family_b ELSE family_a END,
			status, status = 'pending' AND issuer_family = $1,
			created_at, confirmed_at, revoked_at
		FROM family_connections WHERE $1 IN (family_a, family_b)
		ORDER BY created_at DESC, id`, p.FamilyID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Connection, error) {
		var c Connection
		return c, r.Scan(&c.ID, &c.OtherFamilyID, &c.Status, &c.AwaitingUs, &c.CreatedAt, &c.ConfirmedAt, &c.RevokedAt)
	})
}

// ConfirmConnection activates a pending connection. Only the family that
// issued the invitation can confirm it.
func (s *Service) ConfirmConnection(ctx context.Context, p identity.Principal, id string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(id) {
		return identity.ErrNotFound
	}
	now := s.Now()
	tag, err := s.pool.Exec(ctx, `UPDATE family_connections
		SET status = 'active', confirmed_at = $3, updated_at = $3
		WHERE id = $1 AND issuer_family = $2 AND status = 'pending'`, id, p.FamilyID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrNotFound
	}
	return nil
}

// RevokeConnection ends a pending or active connection; either family may
// do it at any time. Declining a pending connection is a revocation. All
// relationships between the children of the two families are revoked and
// pending proposals are cancelled. Revoking again is a no-op.
func (s *Service) RevokeConnection(ctx context.Context, p identity.Principal, id string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(id) {
		return identity.ErrNotFound
	}
	now := s.Now()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var famA, famB, status string
		err := tx.QueryRow(ctx, `SELECT family_a, family_b, status FROM family_connections
			WHERE id = $1 AND $2 IN (family_a, family_b) FOR UPDATE`, id, p.FamilyID).Scan(&famA, &famB, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil || status == "revoked" {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE family_connections
			SET status = 'revoked', revoked_at = $2, updated_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		return revokeBetween(ctx, tx, id, famA, famB, p.FamilyID, now)
	})
}
