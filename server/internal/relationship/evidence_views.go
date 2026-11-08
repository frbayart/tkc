package relationship

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/frbayart/tkc/server/internal/evidence"
	"github.com/frbayart/tkc/server/internal/identity"
)

// RelationshipEvents returns the relationship's event timeline, with its
// chain verification. It is the same for both families; only parents see it.
func (s *Service) RelationshipEvents(ctx context.Context, p identity.Principal, id string) (evidence.TimelineView, error) {
	if err := requireParent(p); err != nil {
		return evidence.TimelineView{}, err
	}
	var v evidence.TimelineView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		v, err = evidence.View(ctx, tx, evidence.SubjectRelationship, x.id)
		return err
	})
	return v, err
}

// ConnectionEvents returns the timeline of a family connection and of the
// proposals made through it, for the parents of both families.
func (s *Service) ConnectionEvents(ctx context.Context, p identity.Principal, id string) (evidence.TimelineView, error) {
	if err := requireParent(p); err != nil {
		return evidence.TimelineView{}, err
	}
	if !validID(id) {
		return evidence.TimelineView{}, identity.ErrNotFound
	}
	var v evidence.TimelineView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM family_connections
			WHERE id = $1 AND $2 IN (family_a, family_b))`, id, p.FamilyID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return identity.ErrNotFound
		}
		conn, err := evidence.View(ctx, tx, evidence.SubjectConnection, id)
		if err != nil {
			return err
		}
		proposals, err := collectIDs(ctx, tx, `SELECT id FROM relationship_proposals WHERE connection_id = $1`, id)
		if err != nil {
			return err
		}
		props, err := evidence.View(ctx, tx, evidence.SubjectProposal, proposals...)
		if err != nil {
			return err
		}
		v = evidence.TimelineView{
			Events:     append(conn.Events, props.Events...),
			ChainValid: conn.ChainValid && props.ChainValid,
		}
		return nil
	})
	return v, err
}

// PolicyVersion is one of a family's policy versions for a relationship.
type PolicyVersion struct {
	Version   int             `json:"version"`
	Policy    json.RawMessage `json:"policy"`
	CreatedBy *string         `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
}

// PolicyVersions returns the caller's family policy versions for a
// relationship, oldest first. They are private to the family: the shared
// timeline only shows version numbers.
func (s *Service) PolicyVersions(ctx context.Context, p identity.Principal, id string) ([]PolicyVersion, error) {
	if err := requireParent(p); err != nil {
		return nil, err
	}
	var out []PolicyVersion
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT version, policy, created_by, created_at FROM relationship_policy_versions
			WHERE relationship_id = $1 AND family_id = $2 ORDER BY version`, x.id, p.FamilyID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (PolicyVersion, error) {
			var v PolicyVersion
			var raw []byte
			err := r.Scan(&v.Version, &raw, &v.CreatedBy, &v.CreatedAt)
			v.Policy = raw
			return v, err
		})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, identity.ErrNotFound
	}
	return out, err
}
