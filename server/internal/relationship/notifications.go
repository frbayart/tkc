package relationship

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/frbayart/tkc/server/internal/identity"
)

// Notification is shown to the parents of a family.
type Notification struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	RelationshipID *string    `json:"relationship_id"`
	ChildID        *string    `json:"child_id"`
	CreatedAt      time.Time  `json:"created_at"`
	ReadAt         *time.Time `json:"read_at"`
}

// maxNotifications bounds a listing.
const maxNotifications = 100

// ListNotifications returns the family's most recent notifications, newest
// first. Only parents see them.
func (s *Service) ListNotifications(ctx context.Context, p identity.Principal) ([]Notification, error) {
	if err := requireParent(p); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, kind, relationship_id, child_id, created_at, read_at
		FROM family_notifications WHERE family_id = $1
		ORDER BY created_at DESC, id LIMIT $2`, p.FamilyID, maxNotifications)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Notification, error) {
		var n Notification
		return n, r.Scan(&n.ID, &n.Kind, &n.RelationshipID, &n.ChildID, &n.CreatedAt, &n.ReadAt)
	})
}

// MarkNotificationRead marks a family notification as read. It is shared by
// the family's parents: once one parent read it, it is read for all.
func (s *Service) MarkNotificationRead(ctx context.Context, p identity.Principal, id string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(id) {
		return identity.ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE family_notifications SET read_at = COALESCE(read_at, $3)
		WHERE id = $1 AND family_id = $2`, id, p.FamilyID, s.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrNotFound
	}
	return nil
}
