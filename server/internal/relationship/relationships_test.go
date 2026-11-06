package relationship

import (
	"errors"
	"testing"
	"time"

	"github.com/frbayart/tkc/server/internal/identity"
	"github.com/frbayart/tkc/server/internal/policy"
)

func openPolicy() policy.FamilyPolicy {
	return policy.FamilyPolicy{
		TimeZone:       "UTC", // overwritten with the child's time zone
		Messaging:      policy.MessagingPolicy{Text: true, Images: true},
		Audio:          policy.CallPolicy{Enabled: true},
		Video:          policy.CallPolicy{Enabled: true},
		CallSchedule:   policy.Daily(policy.Clock(8, 0), policy.Clock(20, 0)),
		MaxCallMinutes: 30,
	}
}

func strictPolicy() policy.FamilyPolicy {
	p := openPolicy()
	p.Video.Enabled = false
	p.MaxCallMinutes = 20
	return p
}

// connect establishes an active connection between two families.
func (e env) connect(t *testing.T, issuer, accepter identity.Principal) string {
	t.Helper()
	inv, err := e.rel.CreateInvitation(ctx, issuer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.rel.AcceptInvitation(ctx, accepter, inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rel.ConfirmConnection(ctx, issuer, c.ID); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// exchange runs an in-person exchange from kid1's code to kid2.
func (e env) exchange(t *testing.T, kid1, kid2 identity.Principal) Relationship {
	t.Helper()
	ec, err := e.rel.CreateExchangeCode(ctx, kid1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.rel.RedeemExchangeCode(ctx, kid2, ec.Code)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e env) status(t *testing.T, p identity.Principal, id string) string {
	t.Helper()
	d, err := e.rel.Get(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	return d.Status
}

func TestInPersonExchange(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)

	r := e.exchange(t, ka, kb)
	if r.Status != "proposed" || r.Origin != "in_person" || r.OurChildID != kb.UserID || r.OtherChildID != ka.UserID {
		t.Fatalf("redeemed %+v", r)
	}
	if _, err := e.rel.Effective(ctx, r.ID); !errors.Is(err, ErrNotActive) {
		t.Fatalf("effective before approval: %v", err)
	}

	// Parent A approves with a policy given in UTC: it is stored in the
	// child's time zone.
	ra, err := e.rel.Approve(ctx, pa, r.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if ra.Status != "pending" || !ra.ApprovedByUs || ra.ApprovedByThem {
		t.Fatalf("after A %+v", ra)
	}
	_, err = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	mustErr(t, err, identity.ErrConflict)

	d, err := e.rel.Get(ctx, pa, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.OurPolicy == nil || d.OurPolicy.TimeZone != "Europe/Paris" || d.Effective != nil {
		t.Fatalf("details after A %+v", d)
	}

	// Invalid policy.
	bad := strictPolicy()
	bad.MaxCallMinutes = -1
	_, err = e.rel.Approve(ctx, pb, r.ID, bad)
	mustErr(t, err, identity.ErrInvalid)

	rb, err := e.rel.Approve(ctx, pb, r.ID, strictPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if rb.Status != "active" {
		t.Fatalf("after B %+v", rb)
	}

	// Children see the effective policy, never a raw family policy.
	d, err = e.rel.Get(ctx, ka, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.OurPolicy != nil || d.Effective == nil || d.Effective.Video || !d.Effective.Audio ||
		d.Effective.MaxCallMinutes != 20 || len(d.Effective.AllowedCallPeriods) == 0 {
		t.Fatalf("child details %+v / %+v", d, d.Effective)
	}
	eff, err := e.rel.Effective(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if dec := eff.Authorize(policy.Request{Capability: policy.VideoCall, At: at}); dec.Allowed {
		t.Fatal("video allowed although family B forbids it")
	}

	// Policy update bumps the effective policy.
	stricter := strictPolicy()
	stricter.Audio.Enabled = false
	if err := e.rel.UpdatePolicy(ctx, pb, r.ID, stricter); err != nil {
		t.Fatal(err)
	}
	eff, _ = e.rel.Effective(ctx, r.ID)
	if eff.Audio.Enabled {
		t.Fatal("audio still allowed after policy update")
	}
}

func TestExchangeCodeRules(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, ka2, kb := e.child(t, pa), e.child(t, pa), e.child(t, pb)

	// Only children create and redeem codes.
	_, err := e.rel.CreateExchangeCode(ctx, pa)
	mustErr(t, err, identity.ErrForbidden)
	ec, _ := e.rel.CreateExchangeCode(ctx, ka)
	_, err = e.rel.RedeemExchangeCode(ctx, pb, ec.Code)
	mustErr(t, err, identity.ErrForbidden)

	// Siblings are not linked; the failed attempt does not consume the code.
	_, err = e.rel.RedeemExchangeCode(ctx, ka2, ec.Code)
	mustErr(t, err, identity.ErrInvalid)
	if _, err := e.rel.RedeemExchangeCode(ctx, kb, ec.Code); err != nil {
		t.Fatal(err)
	}
	// Single use.
	_, err = e.rel.RedeemExchangeCode(ctx, kb, ec.Code)
	mustErr(t, err, identity.ErrInvalidCode)

	// One live relationship per pair, whichever child shows the code.
	ec2, _ := e.rel.CreateExchangeCode(ctx, kb)
	_, err = e.rel.RedeemExchangeCode(ctx, ka, ec2.Code)
	mustErr(t, err, identity.ErrConflict)

	// Expiry.
	ec3, _ := e.rel.CreateExchangeCode(ctx, ka2)
	e.clk.advance(DefaultExchangeTTL)
	_, err = e.rel.RedeemExchangeCode(ctx, kb, ec3.Code)
	mustErr(t, err, identity.ErrInvalidCode)
}

func TestSuspendResumeRevoke(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)
	r := e.exchange(t, ka, kb)
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	_, _ = e.rel.Approve(ctx, pb, r.ID, openPolicy())

	// Children cannot suspend.
	mustErr(t, e.rel.Suspend(ctx, ka, r.ID), identity.ErrForbidden)

	if err := e.rel.Suspend(ctx, pb, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rel.Effective(ctx, r.ID); !errors.Is(err, ErrNotActive) {
		t.Fatalf("suspended relationship still effective: %v", err)
	}
	mustErr(t, e.rel.Suspend(ctx, pa, r.ID), identity.ErrConflict)
	// Only the suspending family resumes.
	mustErr(t, e.rel.Resume(ctx, pa, r.ID), identity.ErrConflict)
	if err := e.rel.Resume(ctx, pb, r.ID); err != nil {
		t.Fatal(err)
	}
	if s := e.status(t, pa, r.ID); s != "active" {
		t.Fatalf("status %s after resume", s)
	}

	// Outsiders and siblings cannot see it.
	ka2 := e.child(t, pa)
	_, err := e.rel.Get(ctx, ka2, r.ID)
	mustErr(t, err, identity.ErrNotFound)
	_, err = e.rel.Get(ctx, e.family(t), r.ID)
	mustErr(t, err, identity.ErrNotFound)
	mustErr(t, e.rel.Revoke(ctx, ka2, r.ID), identity.ErrNotFound)

	// A child blocks the relationship; revoking again is a no-op.
	if err := e.rel.Revoke(ctx, kb, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.rel.Revoke(ctx, pa, r.ID); err != nil {
		t.Fatal(err)
	}
	if s := e.status(t, pa, r.ID); s != "revoked" {
		t.Fatalf("status %s after revoke", s)
	}
	mustErr(t, e.rel.UpdatePolicy(ctx, pa, r.ID, openPolicy()), identity.ErrConflict)
	_, err = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	mustErr(t, err, identity.ErrConflict)

	// Children no longer list revoked relationships; parents still do.
	if rs, _ := e.rel.List(ctx, kb); len(rs) != 0 {
		t.Fatalf("child lists %+v", rs)
	}
	if rs, _ := e.rel.List(ctx, pb); len(rs) != 1 || rs[0].Status != "revoked" {
		t.Fatalf("parent lists %+v", rs)
	}

	// After revocation the children can exchange again.
	r2 := e.exchange(t, kb, ka)
	if r2.ID == r.ID || r2.Status != "proposed" {
		t.Fatalf("new relationship %+v", r2)
	}
}

func TestParentProposals(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)

	// No connection yet.
	x := e.family(t)
	_, err := e.rel.Propose(ctx, pa, "00000000-0000-4000-8000-000000000000", ka.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)

	conn := e.connect(t, pa, pb)
	_, err = e.rel.Propose(ctx, x, conn, ka.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)
	// Cannot propose another family's child.
	_, err = e.rel.Propose(ctx, pa, conn, kb.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)

	pr, err := e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}

	in, _ := e.rel.ListProposals(ctx, pb)
	if len(in) != 1 || !in[0].Incoming || in[0].OurChildID != "" || in[0].OtherFamilyID != pa.FamilyID {
		t.Fatalf("incoming %+v (must not reveal the proposed child)", in)
	}
	out, _ := e.rel.ListProposals(ctx, pa)
	if len(out) != 1 || out[0].Incoming || out[0].OurChildID != ka.UserID {
		t.Fatalf("outgoing %+v", out)
	}

	// The proposer cannot accept their own proposal; B must pick its child.
	_, err = e.rel.AcceptProposal(ctx, pa, pr.ID, ka.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)
	_, err = e.rel.AcceptProposal(ctx, pb, pr.ID, ka.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)

	r, err := e.rel.AcceptProposal(ctx, pb, pr.ID, kb.UserID, strictPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "active" || r.Origin != "parent" || !r.ApprovedByUs || !r.ApprovedByThem || r.OtherChildID != ka.UserID {
		t.Fatalf("accepted %+v", r)
	}
	eff, err := e.rel.Effective(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Video.Enabled || eff.MaxCall != 20*time.Minute {
		t.Fatalf("effective %+v", eff)
	}
	_, err = e.rel.AcceptProposal(ctx, pb, pr.ID, kb.UserID, strictPolicy())
	mustErr(t, err, identity.ErrNotFound)

	// A second proposal for the same pair conflicts on acceptance.
	pr2, _ := e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	_, err = e.rel.AcceptProposal(ctx, pb, pr2.ID, kb.UserID, strictPolicy())
	mustErr(t, err, identity.ErrConflict)

	// Declining and cancelling.
	mustErr(t, e.rel.CloseProposal(ctx, x, pr2.ID), identity.ErrNotFound)
	if err := e.rel.CloseProposal(ctx, pb, pr2.ID); err != nil {
		t.Fatal(err)
	}
	mustErr(t, e.rel.CloseProposal(ctx, pa, pr2.ID), identity.ErrNotFound)
	if ps, _ := e.rel.ListProposals(ctx, pa); len(ps) != 0 {
		t.Fatalf("closed proposals still listed %+v", ps)
	}

	// Children cannot use proposals.
	_, err = e.rel.ListProposals(ctx, kb)
	mustErr(t, err, identity.ErrForbidden)
}

func TestConnectionRevocationCascades(t *testing.T) {
	e := newEnv(t)
	pa, pb, pc := e.family(t), e.family(t), e.family(t)
	ka, kb, kc := e.child(t, pa), e.child(t, pb), e.child(t, pc)

	conn := e.connect(t, pa, pb)

	// Parent-path relationship and in-person relationship between A and B.
	pr, _ := e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	r1, err := e.rel.AcceptProposal(ctx, pb, pr.ID, kb.UserID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	kb2 := e.child(t, pb)
	r2 := e.exchange(t, ka, kb2)
	pending, _ := e.rel.Propose(ctx, pb, conn, kb2.UserID, openPolicy())

	// A relationship with a third family must not be affected.
	r3 := e.exchange(t, ka, kc)

	if err := e.rel.RevokeConnection(ctx, pb, conn); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{r1.ID, r2.ID} {
		if s := e.status(t, pa, id); s != "revoked" {
			t.Fatalf("relationship %s is %s after connection revocation", id, s)
		}
	}
	if s := e.status(t, pa, r3.ID); s != "proposed" {
		t.Fatalf("unrelated relationship is %s", s)
	}
	_, err = e.rel.AcceptProposal(ctx, pa, pending.ID, ka.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)
	if ps, _ := e.rel.ListProposals(ctx, pa); len(ps) != 0 {
		t.Fatalf("proposals after revocation %+v", ps)
	}

	// Reconnecting does not restore relationships.
	inv, _ := e.rel.CreateInvitation(ctx, pa)
	c, _ := e.rel.AcceptInvitation(ctx, pb, inv.Code)
	_ = e.rel.ConfirmConnection(ctx, pa, c.ID)
	if s := e.status(t, pa, r1.ID); s != "revoked" {
		t.Fatalf("relationship restored to %s", s)
	}
}

func TestRemovingChildDeletesRelationships(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)
	r := e.exchange(t, ka, kb)
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	_, _ = e.rel.Approve(ctx, pb, r.ID, openPolicy())

	// Give child B a device that has sent a message in the relationship.
	code, _, err := e.ids.CreateEnrollment(ctx, pb, kb.UserID)
	if err != nil {
		t.Fatal(err)
	}
	keys := deviceKeys(t)
	devID, _, err := e.ids.Enroll(ctx, code, keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ids.ApproveDevice(ctx, pb, devID, keys.IdentityPublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rel.pool.Exec(ctx, `INSERT INTO messages (relationship_id, sender_device_id, ciphertext)
		VALUES ($1, $2, 'opaque')`, r.ID, devID); err != nil {
		t.Fatal(err)
	}

	if err := e.ids.RemoveUser(ctx, pb, kb.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = e.rel.Get(ctx, pa, r.ID)
	mustErr(t, err, identity.ErrNotFound)
	if _, err := e.rel.Effective(ctx, r.ID); !errors.Is(err, ErrNotActive) {
		t.Fatalf("effective after removal: %v", err)
	}
	var n int
	if err := e.rel.pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("messages left: %d (%v)", n, err)
	}
}

func TestChildBlockNotifiesParents(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)
	r := e.exchange(t, ka, kb)
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	_, _ = e.rel.Approve(ctx, pb, r.ID, openPolicy())

	if err := e.rel.Revoke(ctx, kb, r.ID); err != nil {
		t.Fatal(err)
	}

	// Child B's parents are notified; the other family is not.
	ns, err := e.rel.ListNotifications(ctx, pb)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].Kind != "relationship_blocked_by_child" ||
		*ns[0].RelationshipID != r.ID || *ns[0].ChildID != kb.UserID || ns[0].ReadAt != nil {
		t.Fatalf("notifications %+v", ns)
	}
	if other, _ := e.rel.ListNotifications(ctx, pa); len(other) != 0 {
		t.Fatalf("other family notified %+v", other)
	}

	// Both families see who revoked and why.
	db, _ := e.rel.Get(ctx, pb, r.ID)
	da, _ := e.rel.Get(ctx, pa, r.ID)
	if db.RevocationReason != "child_block" || !db.RevokedByUs || da.RevocationReason != "child_block" || da.RevokedByUs {
		t.Fatalf("revocation seen by B %+v, by A %+v", db.Relationship, da.Relationship)
	}

	// Children cannot read notifications; only the family's parents mark them.
	_, err = e.rel.ListNotifications(ctx, kb)
	mustErr(t, err, identity.ErrForbidden)
	mustErr(t, e.rel.MarkNotificationRead(ctx, pa, ns[0].ID), identity.ErrNotFound)
	if err := e.rel.MarkNotificationRead(ctx, pb, ns[0].ID); err != nil {
		t.Fatal(err)
	}
	ns, _ = e.rel.ListNotifications(ctx, pb)
	if ns[0].ReadAt == nil {
		t.Fatal("notification not marked read")
	}

	// A parent's revocation does not notify and is labelled as such.
	r2 := e.exchange(t, ka, kb)
	if err := e.rel.Revoke(ctx, pa, r2.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.rel.Get(ctx, pb, r2.ID); d.RevocationReason != "parent" || d.RevokedByUs {
		t.Fatalf("parent revocation %+v", d.Relationship)
	}
	if ns, _ := e.rel.ListNotifications(ctx, pb); len(ns) != 1 {
		t.Fatalf("parent revocation notified %+v", ns)
	}

	// Connection revocation is labelled too.
	conn := e.connect(t, pa, pb)
	pr, _ := e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	r3, _ := e.rel.AcceptProposal(ctx, pb, pr.ID, kb.UserID, openPolicy())
	_ = e.rel.RevokeConnection(ctx, pb, conn)
	if d, _ := e.rel.Get(ctx, pa, r3.ID); d.RevocationReason != "connection_revoked" || d.RevokedByUs {
		t.Fatalf("connection revocation %+v", d.Relationship)
	}
}

// After a family connection is revoked, everything can be rebuilt from
// scratch, like new contacts: a new invitation and confirmation, then new
// relationships with new approvals and policies.
func TestRestartAfterConnectionRevocation(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)

	conn := e.connect(t, pa, pb)
	pr, _ := e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	old, err := e.rel.AcceptProposal(ctx, pb, pr.ID, kb.UserID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rel.RevokeConnection(ctx, pa, conn); err != nil {
		t.Fatal(err)
	}

	// While the families are disconnected, no proposal is possible...
	_, err = e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	mustErr(t, err, identity.ErrNotFound)

	// ...but the children may meet again in person: a brand new relationship
	// that both families must approve again, with fresh policies.
	r := e.exchange(t, kb, ka)
	if r.ID == old.ID || r.Status != "proposed" || r.ApprovedByUs || r.ApprovedByThem {
		t.Fatalf("new in-person relationship %+v", r)
	}
	if d, _ := e.rel.Get(ctx, pa, r.ID); d.OurPolicy != nil {
		t.Fatalf("old policy carried over %+v", d.OurPolicy)
	}
	_ = e.rel.Revoke(ctx, pb, r.ID)

	// Reconnecting requires a new invitation and confirmation.
	inv, _ := e.rel.CreateInvitation(ctx, pb)
	c, err := e.rel.AcceptInvitation(ctx, pa, inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "pending" {
		t.Fatalf("reconnection %+v", c)
	}
	if err := e.rel.ConfirmConnection(ctx, pb, c.ID); err != nil {
		t.Fatal(err)
	}

	// Then a new proposal creates a new relationship; the old one stays
	// revoked as history.
	pr2, err := e.rel.Propose(ctx, pb, c.ID, kb.UserID, strictPolicy())
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := e.rel.AcceptProposal(ctx, pa, pr2.ID, ka.UserID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == old.ID || fresh.Status != "active" {
		t.Fatalf("fresh relationship %+v", fresh)
	}
	if d, _ := e.rel.Get(ctx, pa, old.ID); d.Status != "revoked" || d.RevocationReason != "connection_revoked" {
		t.Fatalf("history %+v", d.Relationship)
	}
	eff, err := e.rel.Effective(ctx, fresh.ID)
	if err != nil || eff.Video.Enabled {
		t.Fatalf("fresh effective policy %+v (%v)", eff, err)
	}
}
