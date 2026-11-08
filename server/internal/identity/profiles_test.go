package identity

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

// signProfile signs a profile version with a device key.
func signProfile(t *testing.T, d device, userID string, version int, previousID string, ct []byte) []byte {
	t.Helper()
	msg, err := ProfileMessage(userID, version, previousID, ct)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.Sign(d.priv, msg)
}

type profileEnv struct {
	s               *Service
	clk             *clock
	parent, child   Principal
	parentD, childD device
	childID         string
}

func newProfileEnv(t *testing.T) profileEnv {
	t.Helper()
	s, c := newService(t)
	pd := newDevice(t)
	founder, err := s.CreateFamily(ctx, "UTC", pd.keys)
	if err != nil {
		t.Fatal(err)
	}
	_, parent := login(t, s, founder.DeviceID, pd)
	childID, _ := s.AddUser(ctx, parent, RoleChild, "UTC")
	cdID, cd := enrollAndApprove(t, s, parent, childID)
	_, child := login(t, s, cdID, cd)
	return profileEnv{s: s, clk: c, parent: parent, child: child, parentD: pd, childD: cd, childID: childID}
}

func TestProfileVersions(t *testing.T) {
	e := newProfileEnv(t)
	s := e.s

	// Paul sets his first profile himself.
	paul := []byte("enc(Paul)")
	v1, err := s.CreateProfile(ctx, e.child, e.childID, "", paul, signProfile(t, e.childD, e.childID, 1, "", paul))
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v1.PreviousID != nil || v1.AuthorDeviceID != e.child.DeviceID {
		t.Fatalf("v1 %+v", v1)
	}

	// He renames himself Polo: a new version linked to the previous one.
	polo := []byte("enc(Polo)")
	v2, err := s.CreateProfile(ctx, e.child, e.childID, v1.ID, polo, signProfile(t, e.childD, e.childID, 2, v1.ID, polo))
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 || v2.PreviousID == nil || *v2.PreviousID != v1.ID {
		t.Fatalf("v2 %+v", v2)
	}

	// A parent can also update the child's profile.
	v3ct := []byte("enc(Polo, avatar)")
	v3, err := s.CreateProfile(ctx, e.parent, e.childID, v2.ID, v3ct, signProfile(t, e.parentD, e.childID, 3, v2.ID, v3ct))
	if err != nil {
		t.Fatal(err)
	}
	if v3.AuthorUserID != e.parent.UserID {
		t.Fatalf("v3 author %+v", v3)
	}

	// The family view shows the current version.
	f, _ := s.Family(ctx, e.child)
	for _, m := range f.Members {
		if m.UserID == e.childID && (m.Profile == nil || m.Profile.ID != v3.ID) {
			t.Fatalf("current profile %+v", m.Profile)
		}
	}

	// The evidence view shows the whole chain, linked both ways.
	h, err := s.ProfileHistory(ctx, e.parent, e.childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || string(h[0].Ciphertext) != "enc(Paul)" || *h[0].NextID != v2.ID ||
		*h[1].PreviousID != v1.ID || *h[1].NextID != v3.ID || h[2].NextID != nil {
		t.Fatalf("history %+v", h)
	}
	_, err = s.ProfileHistory(ctx, e.child, e.childID)
	mustErr(t, err, ErrForbidden)
	other, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	_, err = s.ProfileHistory(ctx, other, e.childID)
	mustErr(t, err, ErrNotFound)
}

func TestProfileConflictsAndSignatures(t *testing.T) {
	e := newProfileEnv(t)
	s := e.s
	ct := []byte("enc(Paul)")
	sig := signProfile(t, e.childD, e.childID, 1, "", ct)

	// Bad signatures: wrong key, wrong version, tampered content.
	_, err := s.CreateProfile(ctx, e.child, e.childID, "", ct, signProfile(t, e.parentD, e.childID, 1, "", ct))
	mustErr(t, err, ErrInvalid)
	_, err = s.CreateProfile(ctx, e.child, e.childID, "", ct, signProfile(t, e.childD, e.childID, 2, "", ct))
	mustErr(t, err, ErrInvalid)
	_, err = s.CreateProfile(ctx, e.child, e.childID, "", []byte("enc(Evil)"), sig)
	mustErr(t, err, ErrInvalid)
	_, err = s.CreateProfile(ctx, e.child, e.childID, "", make([]byte, MaxProfileSize+1), sig)
	mustErr(t, err, ErrInvalid)

	v1, err := s.CreateProfile(ctx, e.child, e.childID, "", ct, sig)
	if err != nil {
		t.Fatal(err)
	}

	// Optimistic concurrency: the previous version must be the current one.
	ct2 := []byte("enc(Polo)")
	_, err = s.CreateProfile(ctx, e.child, e.childID, "", ct2, signProfile(t, e.childD, e.childID, 1, "", ct2))
	mustErr(t, err, ErrConflict)
	v2, err := s.CreateProfile(ctx, e.child, e.childID, v1.ID, ct2, signProfile(t, e.childD, e.childID, 2, v1.ID, ct2))
	if err != nil {
		t.Fatal(err)
	}
	ct3 := []byte("enc(Stale)")
	_, err = s.CreateProfile(ctx, e.child, e.childID, v1.ID, ct3, signProfile(t, e.childD, e.childID, 2, v1.ID, ct3))
	mustErr(t, err, ErrConflict)
	_ = v2

	// Authorization: a child cannot edit a sibling; other families cannot edit.
	siblingID, _ := s.AddUser(ctx, e.parent, RoleChild, "UTC")
	_, err = s.CreateProfile(ctx, e.child, siblingID, "", ct, signProfile(t, e.childD, siblingID, 1, "", ct))
	mustErr(t, err, ErrForbidden)
	od := newDevice(t)
	other, _ := s.CreateFamily(ctx, "UTC", od.keys)
	_, err = s.CreateProfile(ctx, other, e.childID, v2.ID, ct3, signProfile(t, od, e.childID, 3, v2.ID, ct3))
	mustErr(t, err, ErrNotFound)
}

func TestProfilesAreImmutableUntilPurge(t *testing.T) {
	e := newProfileEnv(t)
	s := e.s
	ct := []byte("enc(Paul)")
	v1, err := s.CreateProfile(ctx, e.child, e.childID, "", ct, signProfile(t, e.childD, e.childID, 1, "", ct))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.pool.Exec(ctx, `UPDATE user_profiles SET ciphertext = 'forged' WHERE id = $1`, v1.ID); err == nil {
		t.Fatal("profile version updated")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM user_profiles WHERE id = $1`, v1.ID); err == nil {
		t.Fatal("profile version deleted")
	}

	// Removing the child keeps its profiles as evidence...
	if err := s.RemoveUser(ctx, e.parent, e.childID); err != nil {
		t.Fatal(err)
	}
	h, err := s.ProfileHistory(ctx, e.parent, e.childID)
	if err != nil || len(h) != 1 {
		t.Fatalf("history after removal %+v (%v)", h, err)
	}
	// ...and new versions are refused.
	ct2 := []byte("enc(Polo)")
	_, err = s.CreateProfile(ctx, e.parent, e.childID, v1.ID, ct2, signProfile(t, e.parentD, e.childID, 2, v1.ID, ct2))
	mustErr(t, err, ErrNotFound)

	// The purge deletes them with the user.
	e.clk.advance(DefaultEvidenceRetention + time.Second)
	if n, err := s.PurgeRemovedMembers(ctx); err != nil || n != 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	var left int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM user_profiles`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("profiles left %d (%v)", left, err)
	}
}

func TestMessagesKeepTheSenderProfileVersion(t *testing.T) {
	e := newProfileEnv(t)
	s := e.s
	paul := []byte("enc(Paul)")
	v1, _ := s.CreateProfile(ctx, e.child, e.childID, "", paul, signProfile(t, e.childD, e.childID, 1, "", paul))

	// A message sent as Paul (messages need a relationship; build a minimal one).
	var otherFamily, otherChild, relID, msgID string
	if err := s.pool.QueryRow(ctx, `INSERT INTO families (created_at) VALUES (now()) RETURNING id`).Scan(&otherFamily); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `INSERT INTO users (family_id, role, created_at) VALUES ($1, 'child', now()) RETURNING id`,
		otherFamily).Scan(&otherChild); err != nil {
		t.Fatal(err)
	}
	a, fa, b, fb := e.childID, e.parent.FamilyID, otherChild, otherFamily
	if b < a {
		a, fa, b, fb = b, fb, a, fa
	}
	if err := s.pool.QueryRow(ctx, `INSERT INTO relationships (child_a, family_a, child_b, family_b, status, origin, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'proposed', 'in_person', now(), now()) RETURNING id`, a, fa, b, fb).Scan(&relID); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `INSERT INTO messages (relationship_id, sender_device_id, sender_profile_id, ciphertext)
		VALUES ($1, $2, $3, 'opaque') RETURNING id`, relID, e.child.DeviceID, v1.ID).Scan(&msgID); err != nil {
		t.Fatal(err)
	}

	// Paul becomes Polo; the message still points to Paul's version.
	polo := []byte("enc(Polo)")
	if _, err := s.CreateProfile(ctx, e.child, e.childID, v1.ID, polo, signProfile(t, e.childD, e.childID, 2, v1.ID, polo)); err != nil {
		t.Fatal(err)
	}
	var sentAs string
	if err := s.pool.QueryRow(ctx, `SELECT sender_profile_id FROM messages WHERE id = $1`, msgID).Scan(&sentAs); err != nil {
		t.Fatal(err)
	}
	if sentAs != v1.ID {
		t.Fatalf("message sent as %s, want %s", sentAs, v1.ID)
	}
}

func TestMemberTimeline(t *testing.T) {
	e := newProfileEnv(t)
	s := e.s
	ct := []byte("enc(Paul)")
	if _, err := s.CreateProfile(ctx, e.child, e.childID, "", ct, signProfile(t, e.childD, e.childID, 1, "", ct)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(ctx, e.parent, e.child.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveUser(ctx, e.parent, e.childID); err != nil {
		t.Fatal(err)
	}

	// Still available after the removal, as evidence.
	v, err := s.UserEvents(ctx, e.parent, e.childID)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, ev := range v.Events {
		types = append(types, ev.Type)
	}
	want := "member_added,device_enrolled,device_approved,profile_version_created,device_revoked,member_removed"
	if strings.Join(types, ",") != want || !v.ChainValid {
		t.Fatalf("timeline %v valid=%v", types, v.ChainValid)
	}
	// Each event records the acting device.
	if p := v.Events[3]; p.ActorDeviceID == nil || *p.ActorDeviceID != e.child.DeviceID {
		t.Fatalf("profile event actor %+v", p)
	}

	_, err = s.UserEvents(ctx, e.child, e.childID)
	mustErr(t, err, ErrForbidden)
	other, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	_, err = s.UserEvents(ctx, other, e.childID)
	mustErr(t, err, ErrNotFound)
}
