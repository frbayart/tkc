package relationship

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/frbayart/tkc/server/internal/evidence"
	"github.com/frbayart/tkc/server/internal/identity"
)

func eventTypes(v evidence.TimelineView) []string {
	out := make([]string, len(v.Events))
	for i, e := range v.Events {
		out[i] = e.Type
	}
	return out
}

func TestRelationshipTimelineIsSharedAndVerifiable(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)

	r := e.exchange(t, ka, kb)
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	e.clk.advance(time.Minute)
	_, _ = e.rel.Approve(ctx, pb, r.ID, strictPolicy())
	_ = e.rel.UpdatePolicy(ctx, pa, r.ID, strictPolicy())
	_ = e.rel.Suspend(ctx, pb, r.ID)
	_ = e.rel.Resume(ctx, pb, r.ID)
	_ = e.rel.Revoke(ctx, kb, r.ID)

	va, err := e.rel.RelationshipEvents(ctx, pa, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"relationship_created", "relationship_approved", "relationship_approved", "policy_updated",
		"relationship_suspended", "relationship_resumed", "relationship_revoked"}
	if got := eventTypes(va); strings.Join(got, ",") != strings.Join(want, ",") || !va.ChainValid {
		t.Fatalf("timeline %v valid=%v", got, va.ChainValid)
	}
	// Both families see the same history.
	vb, _ := e.rel.RelationshipEvents(ctx, pb, r.ID)
	if len(vb.Events) != len(va.Events) || vb.Events[6].ID != va.Events[6].ID {
		t.Fatal("families see different timelines")
	}
	// The block records the child as actor and the reason.
	last := va.Events[6]
	if last.ActorUserID == nil || *last.ActorUserID != kb.UserID || !strings.Contains(string(last.Data), `"child_block"`) {
		t.Fatalf("block event %+v", last)
	}
	// Shared events never contain a family's raw policy.
	for _, ev := range va.Events {
		if strings.Contains(string(ev.Data), "messaging") || strings.Contains(string(ev.Data), "call_schedule") {
			t.Fatalf("raw policy leaked in %s: %s", ev.Type, ev.Data)
		}
	}

	// Children and outsiders cannot read it.
	_, err = e.rel.RelationshipEvents(ctx, ka, r.ID)
	mustErr(t, err, identity.ErrForbidden)
	_, err = e.rel.RelationshipEvents(ctx, e.family(t), r.ID)
	mustErr(t, err, identity.ErrNotFound)
}

func TestPolicyVersionsArePrivateHistory(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	r := e.exchange(t, e.child(t, pa), e.child(t, pb))
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	_, _ = e.rel.Approve(ctx, pb, r.ID, strictPolicy())
	_ = e.rel.UpdatePolicy(ctx, pa, r.ID, strictPolicy())

	va, err := e.rel.PolicyVersions(ctx, pa, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(va) != 2 || va[0].Version != 1 || va[1].Version != 2 {
		t.Fatalf("A versions %+v", va)
	}
	var v1 struct {
		Video struct{ Enabled bool } `json:"video"`
	}
	if err := json.Unmarshal(va[0].Policy, &v1); err != nil || !v1.Video.Enabled {
		t.Fatalf("A version 1 not kept as it was: %s", va[0].Policy)
	}
	if vb, _ := e.rel.PolicyVersions(ctx, pb, r.ID); len(vb) != 1 {
		t.Fatalf("B versions %+v", vb)
	}

	// Versions are immutable.
	if _, err := e.rel.pool.Exec(ctx, `UPDATE relationship_policy_versions SET policy = '{}'`); err == nil {
		t.Fatal("policy version updated")
	}
	if _, err := e.rel.pool.Exec(ctx, `DELETE FROM relationship_policy_versions`); err == nil {
		t.Fatal("policy version deleted")
	}
}

func TestConnectionTimeline(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)
	conn := e.connect(t, pa, pb)
	pr, _ := e.rel.Propose(ctx, pa, conn, ka.UserID, openPolicy())
	r, err := e.rel.AcceptProposal(ctx, pb, pr.ID, kb.UserID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.rel.Propose(ctx, pb, conn, kb.UserID, openPolicy()); err != nil {
		t.Fatal(err)
	}
	_ = e.rel.RevokeConnection(ctx, pa, conn)

	v, err := e.rel.ConnectionEvents(ctx, pb, conn)
	if err != nil {
		t.Fatal(err)
	}
	if !v.ChainValid {
		t.Fatal("connection chain invalid")
	}
	types := map[string]int{}
	for _, ev := range v.Events {
		types[ev.Type]++
		// Proposal events never reveal the proposed child.
		if ev.SubjectType == evidence.SubjectProposal && strings.Contains(string(ev.Data), ka.UserID) {
			t.Fatalf("proposal event reveals the child: %s", ev.Data)
		}
	}
	for _, typ := range []string{"connection_requested", "connection_confirmed", "connection_revoked",
		"proposal_created", "proposal_accepted", "proposal_cancelled"} {
		if types[typ] == 0 {
			t.Fatalf("missing %s in %v", typ, types)
		}
	}

	// The cascade is recorded in the relationship's own timeline.
	rv, _ := e.rel.RelationshipEvents(ctx, pa, r.ID)
	got := eventTypes(rv)
	if got[0] != "relationship_created" || got[len(got)-1] != "relationship_revoked" || len(got) != 4 {
		t.Fatalf("relationship timeline %v", got)
	}

	_, err = e.rel.ConnectionEvents(ctx, e.family(t), conn)
	mustErr(t, err, identity.ErrNotFound)
}

func TestTamperingIsDetected(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	r := e.exchange(t, e.child(t, pa), e.child(t, pb))
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())

	// The database refuses to alter or delete events.
	if _, err := e.rel.pool.Exec(ctx, `UPDATE evidence_events SET data = '{}'`); err == nil {
		t.Fatal("event updated")
	}
	if _, err := e.rel.pool.Exec(ctx, `DELETE FROM evidence_events`); err == nil {
		t.Fatal("event deleted")
	}

	// Even with the guard bypassed (e.g. by an operator), the chain shows it.
	for _, q := range []string{
		`ALTER TABLE evidence_events DISABLE TRIGGER evidence_events_no_update`,
		`UPDATE evidence_events SET data = '{"forged":true}' WHERE seq = 1`,
		`ALTER TABLE evidence_events ENABLE TRIGGER evidence_events_no_update`,
	} {
		if _, err := e.rel.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	v, err := e.rel.RelationshipEvents(ctx, pa, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.ChainValid {
		t.Fatal("forged event not detected")
	}
}

func TestDatabaseGuardsAgainstEarlyDeletion(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)
	r := e.exchange(t, ka, kb)

	code, _, _ := e.ids.CreateEnrollment(ctx, pa, ka.UserID)
	keys := deviceKeys(t)
	devID, _, _ := e.ids.Enroll(ctx, code, keys)
	if _, err := e.rel.pool.Exec(ctx, `INSERT INTO messages (relationship_id, sender_device_id, ciphertext)
		VALUES ($1, $2, 'opaque')`, r.ID, devID); err != nil {
		t.Fatal(err)
	}

	for name, q := range map[string]string{
		"active account":  `DELETE FROM users WHERE id = '` + ka.UserID + `'`,
		"relationship":    `DELETE FROM relationships WHERE id = '` + r.ID + `'`,
		"message":         `DELETE FROM messages`,
		"message rewrite": `UPDATE messages SET ciphertext = 'forged'`,
	} {
		if _, err := e.rel.pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: early deletion or alteration accepted", name)
		}
	}

	// A removed account is still protected before the end of retention,
	// even by a direct DELETE.
	if err := e.ids.RemoveUser(ctx, pa, ka.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rel.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, ka.UserID); err == nil {
		t.Fatal("removed account deleted before the end of evidence retention")
	}
}

func TestPurgeRemovesWholeChains(t *testing.T) {
	e := newEnv(t)
	pa, pb := e.family(t), e.family(t)
	ka, kb := e.child(t, pa), e.child(t, pb)
	conn := e.connect(t, pa, pb)
	r := e.exchange(t, ka, kb)
	_, _ = e.rel.Approve(ctx, pa, r.ID, openPolicy())
	if err := e.ids.RemoveUser(ctx, pa, ka.UserID); err != nil {
		t.Fatal(err)
	}

	// During retention, the removal is part of the evidence.
	if v, _ := e.rel.RelationshipEvents(ctx, pb, r.ID); eventTypes(v)[len(v.Events)-1] != "relationship_revoked" {
		t.Fatalf("timeline %v", eventTypes(v))
	}

	e.ids.Now = func() time.Time { return e.clk.now().Add(identity.DefaultEvidenceRetention) }
	if n, err := e.ids.PurgeRemovedMembers(ctx); err != nil || n != 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	var left int
	if err := e.rel.pool.QueryRow(ctx, `SELECT count(*) FROM evidence_events
		WHERE subject_id IN ($1, $2)`, ka.UserID, r.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("events of purged subjects left: %d (%v)", left, err)
	}
	// Other chains are untouched and still valid.
	v, err := e.rel.ConnectionEvents(ctx, pb, conn)
	if err != nil || !v.ChainValid || len(v.Events) == 0 {
		t.Fatalf("connection timeline after purge %+v (%v)", v, err)
	}
}
