package relationship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/frbayart/tkc/server/internal/evidence"
	"github.com/frbayart/tkc/server/internal/identity"
	"github.com/frbayart/tkc/server/internal/policy"
)

// DefaultExchangeTTL is the lifetime of an in-person exchange code: the two
// children are physically together, so it is very short.
const DefaultExchangeTTL = 5 * time.Minute

// Relationship is a child relationship as seen by one of its families.
type Relationship struct {
	ID             string `json:"id"`
	OurChildID     string `json:"our_child_id"`
	OtherChildID   string `json:"other_child_id"`
	OtherFamilyID  string `json:"other_family_id"`
	Status         string `json:"status"`
	Origin         string `json:"origin"`
	ApprovedByUs   bool   `json:"approved_by_us"`
	ApprovedByThem bool   `json:"approved_by_them"`
	SuspendedByUs  bool   `json:"suspended_by_us"`
	// RevocationReason is "parent", "child_block" or "connection_revoked"
	// for revoked relationships.
	RevocationReason string `json:"revocation_reason,omitempty"`
	RevokedByUs      bool   `json:"revoked_by_us"`
	// Current profiles of both children, filled in by List and Get.
	OurChildProfile   *identity.Profile `json:"our_child_profile,omitempty"`
	OtherChildProfile *identity.Profile `json:"other_child_profile,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// relRow is a relationships row.
type relRow struct {
	id, childA, familyA, childB, familyB, status, origin string
	approvedA, approvedB                                 *time.Time
	suspendedBy, revokedBy, revocationReason             *string
	createdAt, updatedAt                                 time.Time
}

const relColumns = `id, child_a, family_a, child_b, family_b, status, origin,
	approved_a_at, approved_b_at, suspended_by, revoked_by_family, revocation_reason, created_at, updated_at`

func scanRel(r pgx.Row) (relRow, error) {
	var x relRow
	err := r.Scan(&x.id, &x.childA, &x.familyA, &x.childB, &x.familyB, &x.status, &x.origin,
		&x.approvedA, &x.approvedB, &x.suspendedBy, &x.revokedBy, &x.revocationReason, &x.createdAt, &x.updatedAt)
	return x, err
}

// side returns which side the family is on.
func (x relRow) side(familyID string) policy.Side {
	if x.familyA == familyID {
		return policy.SideA
	}
	return policy.SideB
}

func (x relRow) view(familyID string) Relationship {
	r := Relationship{
		ID: x.id, Status: x.status, Origin: x.origin,
		SuspendedByUs: x.suspendedBy != nil && *x.suspendedBy == familyID,
		RevokedByUs:   x.revokedBy != nil && *x.revokedBy == familyID,
		CreatedAt:     x.createdAt, UpdatedAt: x.updatedAt,
	}
	if x.revocationReason != nil {
		r.RevocationReason = *x.revocationReason
	}
	if x.side(familyID) == policy.SideA {
		r.OurChildID, r.OtherChildID, r.OtherFamilyID = x.childA, x.childB, x.familyB
		r.ApprovedByUs, r.ApprovedByThem = x.approvedA != nil, x.approvedB != nil
	} else {
		r.OurChildID, r.OtherChildID, r.OtherFamilyID = x.childB, x.childA, x.familyA
		r.ApprovedByUs, r.ApprovedByThem = x.approvedB != nil, x.approvedA != nil
	}
	return r
}

// visibleTo reports whether the principal may see the relationship: parents
// see their family's relationships, children only their own.
func (x relRow) visibleTo(p identity.Principal) bool {
	if p.FamilyID != x.familyA && p.FamilyID != x.familyB {
		return false
	}
	return p.Role == identity.RoleParent || p.UserID == x.childA || p.UserID == x.childB
}

// loadRel loads a relationship visible to p, locking it in tx.
func loadRel(ctx context.Context, tx pgx.Tx, p identity.Principal, id string) (relRow, error) {
	if !validID(id) {
		return relRow{}, identity.ErrNotFound
	}
	x, err := scanRel(tx.QueryRow(ctx, `SELECT `+relColumns+` FROM relationships
		WHERE id = $1 AND $2 IN (family_a, family_b) FOR UPDATE`, id, p.FamilyID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !x.visibleTo(p)) {
		return relRow{}, identity.ErrNotFound
	}
	return x, err
}

// childTimeZone returns the time zone of a child of the given family.
func childTimeZone(ctx context.Context, q pgx.Tx, childID, familyID string) (string, error) {
	var tz string
	err := q.QueryRow(ctx, `SELECT time_zone FROM users
		WHERE id = $1 AND family_id = $2 AND role = 'child' AND status = 'active'`, childID, familyID).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", identity.ErrNotFound
	}
	return tz, err
}

// preparePolicy forces the policy's time zone to the child's and validates it.
func preparePolicy(pol policy.FamilyPolicy, tz string) (policy.FamilyPolicy, []byte, error) {
	pol.TimeZone = tz
	if err := pol.Validate(); err != nil {
		return pol, nil, fmt.Errorf("%w: %v", identity.ErrInvalid, err)
	}
	b, err := json.Marshal(pol)
	return pol, b, err
}

// upsertPolicy sets a family's current policy and appends it to the
// family's immutable policy versions. It returns the new version number.
func upsertPolicy(ctx context.Context, tx pgx.Tx, relID, familyID, userID string, pol []byte, now time.Time) (int, error) {
	var version int
	err := tx.QueryRow(ctx, `INSERT INTO relationship_policies (relationship_id, family_id, policy, version, updated_by, updated_at)
		VALUES ($1, $2, $3, 1, $4, $5)
		ON CONFLICT (relationship_id, family_id) DO UPDATE
		SET policy = EXCLUDED.policy, version = relationship_policies.version + 1,
		    updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
		RETURNING version`, relID, familyID, pol, userID, now).Scan(&version)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO relationship_policy_versions
		(relationship_id, family_id, version, policy, created_by, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		relID, familyID, version, pol, userID, now)
	return version, err
}

// insertRel creates a relationship between two children of different
// families, in canonical order. A live relationship for the pair is a
// conflict.
func insertRel(ctx context.Context, tx pgx.Tx, child1, family1, child2, family2, status, origin string,
	approved *time.Time, invitationID *string, now time.Time) (relRow, error) {
	if family1 == family2 {
		return relRow{}, fmt.Errorf("%w: children of the same family", identity.ErrInvalid)
	}
	if child2 < child1 {
		child1, family1, child2, family2 = child2, family2, child1, family1
	}
	x, err := scanRel(tx.QueryRow(ctx, `INSERT INTO relationships
		(child_a, family_a, child_b, family_b, status, origin, approved_a_at, approved_b_at,
		 invitation_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9, $9)
		RETURNING `+relColumns, child1, family1, child2, family2, status, origin, approved, invitationID, now))
	if isUniqueViolation(err) {
		return relRow{}, fmt.Errorf("%w: relationship already exists", identity.ErrConflict)
	}
	return x, err
}

// ---------------------------------------------------------------------------
// In-person exchange

// ExchangeCode is an ephemeral code a child shows as a QR code.
type ExchangeCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateExchangeCode issues a single-use code for the calling child.
func (s *Service) CreateExchangeCode(ctx context.Context, p identity.Principal) (ExchangeCode, error) {
	if p.Role != identity.RoleChild {
		return ExchangeCode{}, identity.ErrForbidden
	}
	now := s.Now()
	ec := ExchangeCode{ExpiresAt: now.Add(s.exchangeTTL())}
	code, hash := newCode()
	_, err := s.pool.Exec(ctx, `INSERT INTO invitations
		(kind, issuer_family_id, issuer_user_id, child_id, token_hash, created_at, expires_at)
		VALUES ('child_exchange', $1, $2, $2, $3, $4, $5)`, p.FamilyID, p.UserID, hash, now, ec.ExpiresAt)
	if err != nil {
		return ExchangeCode{}, err
	}
	ec.Code = code
	return ec, nil
}

func (s *Service) exchangeTTL() time.Duration {
	if s.ExchangeTTL > 0 {
		return s.ExchangeTTL
	}
	return DefaultExchangeTTL
}

// RedeemExchangeCode is called by the second child after scanning the first
// child's code. It creates a relationship awaiting both parents' approval.
func (s *Service) RedeemExchangeCode(ctx context.Context, p identity.Principal, code string) (Relationship, error) {
	if p.Role != identity.RoleChild {
		return Relationship{}, identity.ErrForbidden
	}
	now := s.Now()
	var x relRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var invID, otherChild, otherFamily string
		err := tx.QueryRow(ctx, `SELECT i.id, i.child_id, i.issuer_family_id FROM invitations i
			JOIN users u ON u.id = i.child_id AND u.status = 'active' AND u.role = 'child'
			WHERE i.token_hash = $1 AND i.kind = 'child_exchange'
			AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > $2
			FOR UPDATE OF i`, hashCode(code), now).Scan(&invID, &otherChild, &otherFamily)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrInvalidCode
		}
		if err != nil {
			return err
		}
		x, err = insertRel(ctx, tx, p.UserID, p.FamilyID, otherChild, otherFamily, "proposed", "in_person", nil, &invID, now)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE invitations SET consumed_at = $2, consumed_by_family = $3 WHERE id = $1`,
			invID, now, p.FamilyID); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p), relEvent(x.id, "relationship_created", map[string]any{
			"origin": "in_person", "code_shown_by_child": otherChild, "code_scanned_by_child": p.UserID,
			"child_a": x.childA, "family_a": x.familyA, "child_b": x.childB, "family_b": x.familyB}))
	})
	if err != nil {
		return Relationship{}, err
	}
	return x.view(p.FamilyID), nil
}

// ---------------------------------------------------------------------------
// Parental approval and lifecycle

// Approve records the caller's family approval of a proposed or pending
// relationship, together with its policy for the relationship. The
// relationship becomes active once both families approved.
func (s *Service) Approve(ctx context.Context, p identity.Principal, id string, pol policy.FamilyPolicy) (Relationship, error) {
	if err := requireParent(p); err != nil {
		return Relationship{}, err
	}
	now := s.Now()
	var x relRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if x, err = loadRel(ctx, tx, p, id); err != nil {
			return err
		}
		if x.status != "proposed" && x.status != "pending" {
			return fmt.Errorf("%w: relationship is %s", identity.ErrConflict, x.status)
		}
		ours := x.view(p.FamilyID)
		if ours.ApprovedByUs {
			return fmt.Errorf("%w: already approved", identity.ErrConflict)
		}
		tz, err := childTimeZone(ctx, tx, ours.OurChildID, p.FamilyID)
		if err != nil {
			return err
		}
		_, raw, err := preparePolicy(pol, tz)
		if err != nil {
			return err
		}
		version, err := upsertPolicy(ctx, tx, x.id, p.FamilyID, p.UserID, raw, now)
		if err != nil {
			return err
		}
		col := "approved_a_at"
		if x.side(p.FamilyID) == policy.SideB {
			col = "approved_b_at"
		}
		status := "pending"
		if ours.ApprovedByThem {
			status = "active"
		}
		x, err = scanRel(tx.QueryRow(ctx, `UPDATE relationships SET `+col+` = $2, status = $3, updated_at = $2
			WHERE id = $1 RETURNING `+relColumns, x.id, now, status))
		if err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p), relEvent(x.id, "relationship_approved",
			map[string]any{"family_id": p.FamilyID, "policy_version": version, "status": status}))
	})
	if err != nil {
		return Relationship{}, err
	}
	return x.view(p.FamilyID), nil
}

// UpdatePolicy replaces the caller's family policy for a relationship it has
// already approved. It takes effect immediately for new authorizations.
func (s *Service) UpdatePolicy(ctx context.Context, p identity.Principal, id string, pol policy.FamilyPolicy) error {
	if err := requireParent(p); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		ours := x.view(p.FamilyID)
		if x.status == "revoked" || !ours.ApprovedByUs {
			return fmt.Errorf("%w: relationship is %s", identity.ErrConflict, x.status)
		}
		tz, err := childTimeZone(ctx, tx, ours.OurChildID, p.FamilyID)
		if err != nil {
			return err
		}
		_, raw, err := preparePolicy(pol, tz)
		if err != nil {
			return err
		}
		now := s.Now()
		version, err := upsertPolicy(ctx, tx, x.id, p.FamilyID, p.UserID, raw, now)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE relationships SET updated_at = $2 WHERE id = $1`, x.id, now); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p), relEvent(x.id, "policy_updated",
			map[string]any{"family_id": p.FamilyID, "policy_version": version}))
	})
}

// Suspend temporarily blocks an active relationship. Only the suspending
// family can resume it.
func (s *Service) Suspend(ctx context.Context, p identity.Principal, id string) error {
	return s.transition(ctx, p, id, "relationship_suspended", func(x relRow) (string, []any, error) {
		if x.status != "active" {
			return "", nil, fmt.Errorf("%w: relationship is %s", identity.ErrConflict, x.status)
		}
		return `status = 'suspended', suspended_by = $3`, []any{p.FamilyID}, nil
	})
}

// Resume reactivates a relationship suspended by the caller's family.
func (s *Service) Resume(ctx context.Context, p identity.Principal, id string) error {
	return s.transition(ctx, p, id, "relationship_resumed", func(x relRow) (string, []any, error) {
		if x.status != "suspended" || *x.suspendedBy != p.FamilyID {
			return "", nil, fmt.Errorf("%w: not suspended by this family", identity.ErrConflict)
		}
		return `status = 'active', suspended_by = NULL`, nil, nil
	})
}

// transition applies a parent-initiated status change. f returns the SET
// clause, which may use $3... for its extra arguments.
func (s *Service) transition(ctx context.Context, p identity.Principal, id, eventType string, f func(relRow) (string, []any, error)) error {
	if err := requireParent(p); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		set, extra, err := f(x)
		if err != nil {
			return err
		}
		now := s.Now()
		args := append([]any{x.id, now}, extra...)
		if _, err = tx.Exec(ctx, `UPDATE relationships SET `+set+`, updated_at = $2 WHERE id = $1`, args...); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p), relEvent(x.id, eventType, map[string]any{"by_family": p.FamilyID}))
	})
}

// Revoke ends a relationship immediately. Parents of either family can
// revoke (or decline) it. A child can revoke (block) their own relationship
// alone; their parents are then notified so they can talk about it with the
// child. Revoking is idempotent.
func (s *Service) Revoke(ctx context.Context, p identity.Principal, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil || x.status == "revoked" {
			return err
		}
		now := s.Now()
		reason := "parent"
		if p.Role != identity.RoleParent {
			reason = "child_block"
		}
		if _, err = tx.Exec(ctx, `UPDATE relationships SET status = 'revoked', revoked_at = $2, updated_at = $2,
			suspended_by = NULL, revoked_by_family = $3, revocation_reason = $4 WHERE id = $1`,
			x.id, now, p.FamilyID, reason); err != nil {
			return err
		}
		if err := evidence.Append(ctx, tx, now, actorOf(p), relEvent(x.id, "relationship_revoked",
			map[string]any{"reason": reason, "by_family": p.FamilyID, "previous_status": x.status})); err != nil {
			return err
		}
		if reason != "child_block" {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO family_notifications (family_id, kind, relationship_id, child_id, created_at)
			VALUES ($1, 'relationship_blocked_by_child', $2, $3, $4)`, p.FamilyID, x.id, p.UserID, now)
		return err
	})
}

// revokeBetween revokes all relationships between two families and cancels
// pending proposals of their connection. Used when byFamily revokes the
// family connection.
func revokeBetween(ctx context.Context, tx pgx.Tx, connectionID, f1, f2, byFamily string, now time.Time) ([]evidence.Entry, error) {
	rels, err := collectIDs(ctx, tx, `UPDATE relationships SET status = 'revoked', revoked_at = $3, updated_at = $3,
		suspended_by = NULL, revoked_by_family = $4, revocation_reason = 'connection_revoked'
		WHERE status <> 'revoked' AND ((family_a = $1 AND family_b = $2) OR (family_a = $2 AND family_b = $1))
		RETURNING id`, f1, f2, now, byFamily)
	if err != nil {
		return nil, err
	}
	props, err := collectIDs(ctx, tx, `UPDATE relationship_proposals SET status = 'cancelled', updated_at = $2
		WHERE connection_id = $1 AND status = 'pending' RETURNING id`, connectionID, now)
	if err != nil {
		return nil, err
	}
	var events []evidence.Entry
	for _, id := range rels {
		events = append(events, relEvent(id, "relationship_revoked",
			map[string]any{"reason": "connection_revoked", "by_family": byFamily, "connection_id": connectionID}))
	}
	for _, id := range props {
		events = append(events, proposalEvent(id, "proposal_cancelled", map[string]any{"reason": "connection_revoked"}))
	}
	return events, nil
}

// List returns the relationships visible to the caller: all of the family's
// for a parent, the child's own non-revoked ones for a child.
func (s *Service) List(ctx context.Context, p identity.Principal) ([]Relationship, error) {
	q := `SELECT ` + relColumns + ` FROM relationships WHERE $1 IN (family_a, family_b)`
	args := []any{p.FamilyID}
	if p.Role != identity.RoleParent {
		q += ` AND $2 IN (child_a, child_b) AND status <> 'revoked'`
		args = append(args, p.UserID)
	}
	rows, err := s.pool.Query(ctx, q+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	xs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (relRow, error) { return scanRel(r) })
	if err != nil {
		return nil, err
	}
	out := make([]Relationship, len(xs))
	for i, x := range xs {
		out[i] = x.view(p.FamilyID)
	}
	if err := attachProfiles(ctx, s.pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// EffectiveView is the effective policy shown to parents and children.
type EffectiveView struct {
	Messaging                 policy.MessagingPolicy `json:"messaging"`
	Audio                     bool                   `json:"audio"`
	Video                     bool                   `json:"video"`
	MaxCallMinutes            int                    `json:"max_call_minutes"`
	RequireIncomingAcceptance bool                   `json:"require_incoming_acceptance"`
	Recording                 policy.RecordingPolicy `json:"recording"`
	// AllowedCallPeriods are the next 7 days' call windows, as absolute times.
	AllowedCallPeriods []policy.Interval `json:"allowed_call_periods"`
}

// Details is a relationship with the caller's family policy and, once both
// families approved, the effective policy.
type Details struct {
	Relationship
	OurPolicy *policy.FamilyPolicy `json:"our_policy,omitempty"`
	// OurHold is the caller's family preservation hold (parents only).
	OurHold   *Hold          `json:"our_hold,omitempty"`
	Effective *EffectiveView `json:"effective,omitempty"`
}

// Get returns a relationship visible to the caller. A family's own raw
// policy is only shown to its parents; the other family's policy is never
// shown, only its intersection with ours.
func (s *Service) Get(ctx context.Context, p identity.Principal, id string) (Details, error) {
	var d Details
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadRel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		d.Relationship = x.view(p.FamilyID)
		rs := []Relationship{d.Relationship}
		if err := attachProfiles(ctx, tx, rs); err != nil {
			return err
		}
		d.Relationship = rs[0]
		pols, err := loadPolicies(ctx, tx, x)
		if err != nil {
			return err
		}
		if p.Role == identity.RoleParent {
			d.OurPolicy = pols[x.side(p.FamilyID)]
			if d.OurHold, err = ourHold(ctx, tx, x.id, p.FamilyID, s.Now()); err != nil {
				return err
			}
		}
		if pols[0] != nil && pols[1] != nil {
			e, err := policy.Intersect(*pols[0], *pols[1])
			if err != nil {
				return err
			}
			now := s.Now()
			d.Effective = &EffectiveView{
				Messaging: e.Messaging, Audio: e.Audio.Enabled, Video: e.Video.Enabled && e.Audio.Enabled,
				MaxCallMinutes:            int(e.MaxCall / time.Minute),
				RequireIncomingAcceptance: e.RequireIncomingAcceptance,
				Recording:                 e.Recording,
				AllowedCallPeriods:        nonNilIntervals(e.AllowedCallPeriods(now, now.Add(7*24*time.Hour))),
			}
		}
		return nil
	})
	return d, err
}

func nonNilIntervals(iv []policy.Interval) []policy.Interval {
	if iv == nil {
		return []policy.Interval{}
	}
	return iv
}

// loadPolicies returns the A and B family policies (nil if not set).
func loadPolicies(ctx context.Context, tx pgx.Tx, x relRow) ([2]*policy.FamilyPolicy, error) {
	var out [2]*policy.FamilyPolicy
	rows, err := tx.Query(ctx, `SELECT family_id, policy FROM relationship_policies WHERE relationship_id = $1`, x.id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var fam string
		var raw []byte
		if err := rows.Scan(&fam, &raw); err != nil {
			return out, err
		}
		var pol policy.FamilyPolicy
		if err := json.Unmarshal(raw, &pol); err != nil {
			return out, err
		}
		out[x.side(fam)] = &pol
	}
	return out, rows.Err()
}

// ErrNotActive is returned by Effective for relationships that do not
// currently allow any communication.
var ErrNotActive = errors.New("relationship not active")

// Effective returns the effective policy of an active relationship, for
// enforcement by messaging and signaling. The caller must already have
// checked that the requesting user is a participant.
func (s *Service) Effective(ctx context.Context, relationshipID string) (policy.Effective, error) {
	if !validID(relationshipID) {
		return policy.Effective{}, ErrNotActive
	}
	var e policy.Effective
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := scanRel(tx.QueryRow(ctx, `SELECT `+relColumns+` FROM relationships WHERE id = $1`, relationshipID))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && x.status != "active") {
			return ErrNotActive
		}
		if err != nil {
			return err
		}
		pols, err := loadPolicies(ctx, tx, x)
		if err != nil {
			return err
		}
		if pols[0] == nil || pols[1] == nil {
			return ErrNotActive
		}
		e, err = policy.Intersect(*pols[0], *pols[1])
		return err
	})
	return e, err
}

// ---------------------------------------------------------------------------
// Parent proposals

// Proposal is a parent's proposal of one of their children to a connected
// family.
type Proposal struct {
	ID            string    `json:"id"`
	ConnectionID  string    `json:"connection_id"`
	Incoming      bool      `json:"incoming"`
	OurChildID    string    `json:"our_child_id,omitempty"` // outgoing only
	OtherFamilyID string    `json:"other_family_id"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// Propose offers one of the caller's children to the other family of an
// active connection, with the caller's family policy.
func (s *Service) Propose(ctx context.Context, p identity.Principal, connectionID, childID string, pol policy.FamilyPolicy) (Proposal, error) {
	if err := requireParent(p); err != nil {
		return Proposal{}, err
	}
	if !validID(connectionID) || !validID(childID) {
		return Proposal{}, identity.ErrNotFound
	}
	now := s.Now()
	pr := Proposal{ConnectionID: connectionID, OurChildID: childID, Status: "pending", CreatedAt: now}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT CASE WHEN family_a = $2 THEN family_b ELSE family_a END
			FROM family_connections WHERE id = $1 AND $2 IN (family_a, family_b) AND status = 'active'
			FOR SHARE`, connectionID, p.FamilyID).Scan(&pr.OtherFamilyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return err
		}
		tz, err := childTimeZone(ctx, tx, childID, p.FamilyID)
		if err != nil {
			return err
		}
		_, raw, err := preparePolicy(pol, tz)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO relationship_proposals
			(connection_id, proposer_family, proposer_child, proposer_user, target_family, proposer_policy,
			 status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $7) RETURNING id`,
			connectionID, p.FamilyID, childID, p.UserID, pr.OtherFamilyID, raw, now).Scan(&pr.ID); err != nil {
			return err
		}
		// The proposed child is not recorded here: proposal events are visible
		// to the target family, which must not learn it before accepting.
		return evidence.Append(ctx, tx, now, actorOf(p), proposalEvent(pr.ID, "proposal_created",
			map[string]any{"connection_id": connectionID, "proposer_family": p.FamilyID, "target_family": pr.OtherFamilyID}))
	})
	if err != nil {
		return Proposal{}, err
	}
	return pr, nil
}

// ListProposals returns the caller's family pending proposals, incoming and
// outgoing. Incoming proposals do not reveal the proposed child.
func (s *Service) ListProposals(ctx context.Context, p identity.Principal) ([]Proposal, error) {
	if err := requireParent(p); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, connection_id, target_family = $1,
			CASE WHEN target_family = $1 THEN '' ELSE proposer_child::text END,
			CASE WHEN target_family = $1 THEN proposer_family ELSE target_family END,
			status, created_at
		FROM relationship_proposals
		WHERE $1 IN (proposer_family, target_family) AND status = 'pending'
		ORDER BY created_at DESC, id`, p.FamilyID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Proposal, error) {
		var pr Proposal
		return pr, r.Scan(&pr.ID, &pr.ConnectionID, &pr.Incoming, &pr.OurChildID, &pr.OtherFamilyID, &pr.Status, &pr.CreatedAt)
	})
}

// AcceptProposal accepts an incoming proposal by choosing one of the
// caller's children and the caller's family policy. Both families have then
// approved, so the relationship is created active.
func (s *Service) AcceptProposal(ctx context.Context, p identity.Principal, proposalID, childID string, pol policy.FamilyPolicy) (Relationship, error) {
	if err := requireParent(p); err != nil {
		return Relationship{}, err
	}
	if !validID(proposalID) || !validID(childID) {
		return Relationship{}, identity.ErrNotFound
	}
	now := s.Now()
	var x relRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var connID, propFamily, propChild, propUser string
		var propPolicy []byte
		err := tx.QueryRow(ctx, `SELECT r.connection_id, r.proposer_family, r.proposer_child, r.proposer_user, r.proposer_policy
			FROM relationship_proposals r
			JOIN family_connections c ON c.id = r.connection_id AND c.status = 'active'
			JOIN users u ON u.id = r.proposer_child AND u.status = 'active'
			WHERE r.id = $1 AND r.target_family = $2 AND r.status = 'pending'
			FOR UPDATE OF r`, proposalID, p.FamilyID).Scan(&connID, &propFamily, &propChild, &propUser, &propPolicy)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return err
		}
		tz, err := childTimeZone(ctx, tx, childID, p.FamilyID)
		if err != nil {
			return err
		}
		_, raw, err := preparePolicy(pol, tz)
		if err != nil {
			return err
		}
		x, err = insertRel(ctx, tx, childID, p.FamilyID, propChild, propFamily, "active", "parent", &now, nil, now)
		if err != nil {
			return err
		}
		ourVersion, err := upsertPolicy(ctx, tx, x.id, p.FamilyID, p.UserID, raw, now)
		if err != nil {
			return err
		}
		theirVersion, err := upsertPolicy(ctx, tx, x.id, propFamily, propUser, propPolicy, now)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE relationship_proposals SET status = 'accepted', relationship_id = $2, updated_at = $3
			WHERE id = $1`, proposalID, x.id, now); err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p),
			proposalEvent(proposalID, "proposal_accepted", map[string]any{"relationship_id": x.id, "by_family": p.FamilyID}),
			relEvent(x.id, "relationship_created", map[string]any{"origin": "parent", "proposal_id": proposalID,
				"proposed_by_user": propUser, "child_a": x.childA, "family_a": x.familyA, "child_b": x.childB, "family_b": x.familyB}),
			relEvent(x.id, "relationship_approved", map[string]any{"family_id": propFamily, "policy_version": theirVersion,
				"status": "active", "via_proposal": true}),
			relEvent(x.id, "relationship_approved", map[string]any{"family_id": p.FamilyID, "policy_version": ourVersion,
				"status": "active"}))
	})
	if err != nil {
		return Relationship{}, err
	}
	return x.view(p.FamilyID), nil
}

// CloseProposal declines an incoming proposal or cancels an outgoing one.
func (s *Service) CloseProposal(ctx context.Context, p identity.Principal, proposalID string) error {
	if err := requireParent(p); err != nil {
		return err
	}
	if !validID(proposalID) {
		return identity.ErrNotFound
	}
	now := s.Now()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `UPDATE relationship_proposals
			SET status = CASE WHEN target_family = $2 THEN 'declined' ELSE 'cancelled' END, updated_at = $3
			WHERE id = $1 AND $2 IN (proposer_family, target_family) AND status = 'pending'
			RETURNING status`, proposalID, p.FamilyID, now).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return err
		}
		return evidence.Append(ctx, tx, now, actorOf(p),
			proposalEvent(proposalID, "proposal_"+status, map[string]any{"by_family": p.FamilyID}))
	})
}

// attachProfiles fills in the children's current profiles.
func attachProfiles(ctx context.Context, q identity.Querier, rs []Relationship) error {
	ids := make([]string, 0, 2*len(rs))
	for _, r := range rs {
		ids = append(ids, r.OurChildID, r.OtherChildID)
	}
	profiles, err := identity.CurrentProfiles(ctx, q, ids)
	if err != nil {
		return err
	}
	for i := range rs {
		rs[i].OurChildProfile = profiles[rs[i].OurChildID]
		rs[i].OtherChildProfile = profiles[rs[i].OtherChildID]
	}
	return nil
}
