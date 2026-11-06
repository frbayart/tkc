package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/frbayart/tkc/server/internal/store/storetest"
)

var ctx = context.Background()

type device struct {
	priv ed25519.PrivateKey
	keys DeviceKeys
}

func newDevice(t *testing.T) device {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc := []byte("enc-key-" + string(pub[:8]))
	return device{priv: priv, keys: DeviceKeys{
		DeviceType:          "phone",
		Platform:            "android",
		IdentityPublicKey:   pub,
		EncryptionPublicKey: enc,
		Proof:               ed25519.Sign(priv, ProofMessage(enc)),
	}}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	s := New(storetest.Pool(t))
	c := &clock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	s.Now = c.now
	return s, c
}

// login authenticates a device and returns its token and principal.
func login(t *testing.T, s *Service, deviceID string, d device) (string, Principal) {
	t.Helper()
	nonce, _, err := s.Challenge(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := ChallengeMessage(deviceID, nonce)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(ctx, deviceID, nonce, ed25519.Sign(d.priv, msg))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return token, p
}

// enrollAndApprove adds a device for userID and approves it as parent.
func enrollAndApprove(t *testing.T, s *Service, parent Principal, userID string) (string, device) {
	t.Helper()
	code, _, err := s.CreateEnrollment(ctx, parent, userID)
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	id, gotUser, err := s.Enroll(ctx, code, d.keys)
	if err != nil {
		t.Fatal(err)
	}
	if gotUser != userID {
		t.Fatalf("enrolled for %s, want %s", gotUser, userID)
	}
	if err := s.ApproveDevice(ctx, parent, id, d.keys.IdentityPublicKey); err != nil {
		t.Fatal(err)
	}
	return id, d
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestFamilyLifecycle(t *testing.T) {
	s, _ := newService(t)
	d1 := newDevice(t)
	founder, err := s.CreateFamily(ctx, "Europe/Paris", d1.keys)
	if err != nil {
		t.Fatal(err)
	}
	_, parent := login(t, s, founder.DeviceID, d1)
	if parent != founder {
		t.Fatalf("principal %+v, want %+v", parent, founder)
	}

	childID, err := s.AddUser(ctx, parent, RoleChild, "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	childDeviceID, cd := enrollAndApprove(t, s, parent, childID)
	_, child := login(t, s, childDeviceID, cd)
	if child.Role != RoleChild || child.FamilyID != parent.FamilyID || child.UserID != childID {
		t.Fatalf("child principal %+v", child)
	}

	// Children cannot administer the family.
	_, err = s.AddUser(ctx, child, RoleChild, "UTC")
	mustErr(t, err, ErrForbidden)
	_, _, err = s.CreateEnrollment(ctx, child, childID)
	mustErr(t, err, ErrForbidden)

	// Second guardian.
	guardianID, err := s.AddUser(ctx, parent, RoleParent, "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	gDeviceID, gd := enrollAndApprove(t, s, parent, guardianID)
	_, guardian := login(t, s, gDeviceID, gd)
	if guardian.Role != RoleParent {
		t.Fatalf("guardian role %s", guardian.Role)
	}

	f, err := s.Family(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Members) != 3 {
		t.Fatalf("family has %d members, want 3", len(f.Members))
	}
	for _, m := range f.Members {
		if len(m.Devices) != 1 || m.Devices[0].Status != "active" {
			t.Fatalf("member %s devices %+v", m.UserID, m.Devices)
		}
	}
	for _, m := range f.Members {
		if m.UserID != childID {
			continue
		}
		if got := m.Devices[0].ApprovedByDeviceID; got == nil || *got != founder.DeviceID {
			t.Fatalf("child device approved by %v, want %s", got, founder.DeviceID)
		}
	}
}

func TestPendingDeviceCannotAuthenticate(t *testing.T) {
	s, _ := newService(t)
	d1 := newDevice(t)
	parent, err := s.CreateFamily(ctx, "UTC", d1.keys)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := s.CreateEnrollment(ctx, parent, parent.UserID)
	if err != nil {
		t.Fatal(err)
	}
	d2 := newDevice(t)
	id, _, err := s.Enroll(ctx, code, d2.keys)
	if err != nil {
		t.Fatal(err)
	}
	nonce, _, err := s.Challenge(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := ChallengeMessage(id, nonce)
	_, _, err = s.CreateSession(ctx, id, nonce, ed25519.Sign(d2.priv, msg))
	mustErr(t, err, ErrUnauthenticated)
}

func TestApprovalRequiresMatchingKeyAndFamily(t *testing.T) {
	s, _ := newService(t)
	d1 := newDevice(t)
	parent, _ := s.CreateFamily(ctx, "UTC", d1.keys)
	other, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)

	code, _, _ := s.CreateEnrollment(ctx, parent, parent.UserID)
	d2 := newDevice(t)
	id, _, err := s.Enroll(ctx, code, d2.keys)
	if err != nil {
		t.Fatal(err)
	}
	mustErr(t, s.ApproveDevice(ctx, parent, id, newDevice(t).keys.IdentityPublicKey), ErrKeyMismatch)
	mustErr(t, s.ApproveDevice(ctx, other, id, d2.keys.IdentityPublicKey), ErrNotFound)
	if err := s.ApproveDevice(ctx, parent, id, d2.keys.IdentityPublicKey); err != nil {
		t.Fatal(err)
	}
	// Already active: no longer pending.
	mustErr(t, s.ApproveDevice(ctx, parent, id, d2.keys.IdentityPublicKey), ErrNotFound)
}

func TestEnrollmentCodes(t *testing.T) {
	s, c := newService(t)
	parent, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	other, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)

	// Cannot enroll for another family's user.
	_, _, err := s.CreateEnrollment(ctx, parent, other.UserID)
	mustErr(t, err, ErrNotFound)

	code, _, _ := s.CreateEnrollment(ctx, parent, parent.UserID)
	if _, _, err := s.Enroll(ctx, code, newDevice(t).keys); err != nil {
		t.Fatal(err)
	}
	// Single use.
	_, _, err = s.Enroll(ctx, code, newDevice(t).keys)
	mustErr(t, err, ErrInvalidCode)

	// Expiry.
	code, _, _ = s.CreateEnrollment(ctx, parent, parent.UserID)
	c.advance(DefaultEnrollmentTTL)
	_, _, err = s.Enroll(ctx, code, newDevice(t).keys)
	mustErr(t, err, ErrInvalidCode)

	// Garbage.
	_, _, err = s.Enroll(ctx, "not-a-code", newDevice(t).keys)
	mustErr(t, err, ErrInvalidCode)
}

func TestEnrollmentInvalidAfterIssuerRevoked(t *testing.T) {
	s, _ := newService(t)
	founder, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	secondID, sd := enrollAndApprove(t, s, founder, founder.UserID)
	_, second := login(t, s, secondID, sd)

	code, _, err := s.CreateEnrollment(ctx, founder, founder.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(ctx, second, founder.DeviceID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Enroll(ctx, code, newDevice(t).keys)
	mustErr(t, err, ErrInvalidCode)
}

func TestKeyValidation(t *testing.T) {
	s, _ := newService(t)
	good := newDevice(t)

	bad := good.keys
	bad.Proof = ed25519.Sign(newDevice(t).priv, ProofMessage(bad.EncryptionPublicKey))
	_, err := s.CreateFamily(ctx, "UTC", bad)
	mustErr(t, err, ErrInvalid)

	bad = good.keys
	bad.Platform = "symbian"
	_, err = s.CreateFamily(ctx, "UTC", bad)
	mustErr(t, err, ErrInvalid)

	_, err = s.CreateFamily(ctx, "Mars/Olympus", good.keys)
	mustErr(t, err, ErrInvalid)

	if _, err := s.CreateFamily(ctx, "UTC", good.keys); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateFamily(ctx, "UTC", good.keys)
	mustErr(t, err, ErrConflict)
}

func TestChallengeSingleUseAndExpiry(t *testing.T) {
	s, c := newService(t)
	d := newDevice(t)
	p, _ := s.CreateFamily(ctx, "UTC", d.keys)

	sign := func(nonce []byte) []byte {
		msg, _ := ChallengeMessage(p.DeviceID, nonce)
		return ed25519.Sign(d.priv, msg)
	}

	nonce, _, _ := s.Challenge(ctx, p.DeviceID)
	if _, _, err := s.CreateSession(ctx, p.DeviceID, nonce, sign(nonce)); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateSession(ctx, p.DeviceID, nonce, sign(nonce))
	mustErr(t, err, ErrUnauthenticated)

	// A wrong signature burns the challenge.
	nonce, _, _ = s.Challenge(ctx, p.DeviceID)
	_, _, err = s.CreateSession(ctx, p.DeviceID, nonce, sign([]byte("other")))
	mustErr(t, err, ErrUnauthenticated)
	_, _, err = s.CreateSession(ctx, p.DeviceID, nonce, sign(nonce))
	mustErr(t, err, ErrUnauthenticated)

	nonce, _, _ = s.Challenge(ctx, p.DeviceID)
	c.advance(DefaultChallengeTTL)
	_, _, err = s.CreateSession(ctx, p.DeviceID, nonce, sign(nonce))
	mustErr(t, err, ErrUnauthenticated)

	// Unknown devices get a nonce too, which is useless.
	unknown := "00000000-0000-4000-8000-000000000000"
	nonce, _, err = s.Challenge(ctx, unknown)
	if err != nil || len(nonce) != NonceSize {
		t.Fatalf("challenge for unknown device: %v", err)
	}
	_, _, err = s.CreateSession(ctx, unknown, nonce, sign(nonce))
	mustErr(t, err, ErrUnauthenticated)
}

func TestSessionExpiryAndLogout(t *testing.T) {
	s, c := newService(t)
	d := newDevice(t)
	p, _ := s.CreateFamily(ctx, "UTC", d.keys)

	token, _ := login(t, s, p.DeviceID, d)
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	_, err := s.Authenticate(ctx, token)
	mustErr(t, err, ErrUnauthenticated)

	token, _ = login(t, s, p.DeviceID, d)
	c.advance(DefaultSessionTTL)
	_, err = s.Authenticate(ctx, token)
	mustErr(t, err, ErrUnauthenticated)

	_, err = s.Authenticate(ctx, "garbage")
	mustErr(t, err, ErrUnauthenticated)
}

func TestRevocation(t *testing.T) {
	s, _ := newService(t)
	d1 := newDevice(t)
	founder, _ := s.CreateFamily(ctx, "UTC", d1.keys)
	_, parent := login(t, s, founder.DeviceID, d1)

	// The only parent device cannot be revoked.
	mustErr(t, s.RevokeDevice(ctx, parent, parent.DeviceID), ErrLastParentDevice)

	childID, _ := s.AddUser(ctx, parent, RoleChild, "UTC")
	c1ID, c1 := enrollAndApprove(t, s, parent, childID)
	c2ID, c2 := enrollAndApprove(t, s, parent, childID)
	c1Token, child1 := login(t, s, c1ID, c1)
	_, child2 := login(t, s, c2ID, c2)

	// A child cannot revoke another device, but can revoke its own.
	mustErr(t, s.RevokeDevice(ctx, child1, c2ID), ErrForbidden)
	_ = child2

	// A parent revokes child device 1: its session dies immediately.
	if err := s.RevokeDevice(ctx, parent, c1ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.Authenticate(ctx, c1Token)
	mustErr(t, err, ErrUnauthenticated)
	nonce, _, _ := s.Challenge(ctx, c1ID)
	msg, _ := ChallengeMessage(c1ID, nonce)
	_, _, err = s.CreateSession(ctx, c1ID, nonce, ed25519.Sign(c1.priv, msg))
	mustErr(t, err, ErrUnauthenticated)

	// Idempotent.
	if err := s.RevokeDevice(ctx, parent, c1ID); err != nil {
		t.Fatal(err)
	}

	// Child revokes its own remaining device.
	if err := s.RevokeDevice(ctx, child2, c2ID); err != nil {
		t.Fatal(err)
	}

	// With a second parent device, the first can be revoked.
	p2ID, p2 := enrollAndApprove(t, s, parent, parent.UserID)
	if err := s.RevokeDevice(ctx, parent, parent.DeviceID); err != nil {
		t.Fatal(err)
	}
	_, parent2 := login(t, s, p2ID, p2)
	mustErr(t, s.RevokeDevice(ctx, parent2, p2ID), ErrLastParentDevice)

	// Other families' devices are invisible.
	other, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	mustErr(t, s.RevokeDevice(ctx, parent2, other.DeviceID), ErrNotFound)
}

func TestRemoveUser(t *testing.T) {
	s, _ := newService(t)
	d1 := newDevice(t)
	founder, _ := s.CreateFamily(ctx, "UTC", d1.keys)
	_, parent := login(t, s, founder.DeviceID, d1)

	childID, _ := s.AddUser(ctx, parent, RoleChild, "UTC")
	cDeviceID, cd := enrollAndApprove(t, s, parent, childID)
	childToken, child := login(t, s, cDeviceID, cd)

	guardianID, _ := s.AddUser(ctx, parent, RoleParent, "UTC")
	gDeviceID, gd := enrollAndApprove(t, s, parent, guardianID)
	_, guardian := login(t, s, gDeviceID, gd)

	// Children cannot remove anyone; other families' members are invisible.
	mustErr(t, s.RemoveUser(ctx, child, childID), ErrForbidden)
	other, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	mustErr(t, s.RemoveUser(ctx, parent, other.UserID), ErrNotFound)
	mustErr(t, s.RemoveUser(ctx, other, childID), ErrNotFound)

	// Removing the child kills its sessions and hides it from the family.
	if err := s.RemoveUser(ctx, parent, childID); err != nil {
		t.Fatal(err)
	}
	_, err := s.Authenticate(ctx, childToken)
	mustErr(t, err, ErrUnauthenticated)
	mustErr(t, s.RemoveUser(ctx, parent, childID), ErrNotFound)

	// The founder, whose device approved the guardian's, can be removed while
	// another parent device remains; the guardian's device stays active.
	// Removed members are hidden from the family.
	if err := s.RemoveUser(ctx, guardian, founder.UserID); err != nil {
		t.Fatal(err)
	}
	f, err := s.Family(ctx, guardian)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Members) != 1 || f.Members[0].UserID != guardianID || f.Members[0].Devices[0].Status != "active" {
		t.Fatalf("family after removals %+v", f)
	}
	// The removed founder's device can no longer authenticate.
	nonce, _, _ := s.Challenge(ctx, founder.DeviceID)
	msg, _ := ChallengeMessage(founder.DeviceID, nonce)
	_, _, err = s.CreateSession(ctx, founder.DeviceID, nonce, ed25519.Sign(d1.priv, msg))
	mustErr(t, err, ErrUnauthenticated)
	// A removed member cannot get new devices.
	_, _, err = s.CreateEnrollment(ctx, guardian, childID)
	mustErr(t, err, ErrNotFound)

	// The last parent cannot be removed.
	mustErr(t, s.RemoveUser(ctx, guardian, guardianID), ErrLastParentDevice)
}

func TestPurgeRemovedMembers(t *testing.T) {
	s, c := newService(t)
	parent, _ := s.CreateFamily(ctx, "UTC", newDevice(t).keys)
	kid, _ := s.AddUser(ctx, parent, RoleChild, "UTC")
	kept, _ := s.AddUser(ctx, parent, RoleChild, "UTC")
	if err := s.RemoveUser(ctx, parent, kid); err != nil {
		t.Fatal(err)
	}
	count := func() (n int) {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id IN ($1, $2)`, kid, kept).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Kept as evidence until the retention period has elapsed.
	c.advance(DefaultEvidenceRetention - time.Minute)
	if n, err := s.PurgeRemovedMembers(ctx); err != nil || n != 0 || count() != 2 {
		t.Fatalf("purged too early: n=%d err=%v", n, err)
	}
	c.advance(time.Minute)
	if n, err := s.PurgeRemovedMembers(ctx); err != nil || n != 1 || count() != 1 {
		t.Fatalf("purge: n=%d err=%v count=%d", n, err, count())
	}
}
