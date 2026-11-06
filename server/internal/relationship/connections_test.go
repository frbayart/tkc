package relationship

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/frbayart/tkc/server/internal/identity"
	"github.com/frbayart/tkc/server/internal/store/storetest"
)

var ctx = context.Background()

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type env struct {
	ids *identity.Service
	rel *Service
	clk *clock
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := storetest.Pool(t)
	return newEnvWithPool(t, pool)
}

func newEnvWithPool(t *testing.T, pool *pgxpool.Pool) env {
	c := &clock{t: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)}
	ids := identity.New(pool)
	ids.Now = c.now
	rel := New(pool)
	rel.Now = c.now
	return env{ids, rel, c}
}

func deviceKeys(t *testing.T) identity.DeviceKeys {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc := append([]byte("enc:"), pub[:8]...)
	return identity.DeviceKeys{
		DeviceType: "phone", Platform: "ios",
		IdentityPublicKey: pub, EncryptionPublicKey: enc,
		Proof: ed25519.Sign(priv, identity.ProofMessage(enc)),
	}
}

// family creates a family and returns its founding parent's principal.
func (e env) family(t *testing.T) identity.Principal {
	t.Helper()
	p, err := e.ids.CreateFamily(ctx, "Europe/Paris", deviceKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e env) child(t *testing.T, parent identity.Principal) identity.Principal {
	t.Helper()
	id, err := e.ids.AddUser(ctx, parent, identity.RoleChild, "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	return identity.Principal{UserID: id, FamilyID: parent.FamilyID, Role: identity.RoleChild}
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func (e env) connections(t *testing.T, p identity.Principal) []Connection {
	t.Helper()
	cs, err := e.rel.ListConnections(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestConnectionFlow(t *testing.T) {
	e := newEnv(t)
	b := e.family(t) // issues the invitation
	a := e.family(t) // accepts it

	inv, err := e.rel.CreateInvitation(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if invs, _ := e.rel.ListInvitations(ctx, b); len(invs) != 1 || invs[0].ID != inv.ID || invs[0].Code != "" {
		t.Fatalf("invitations %+v", invs)
	}

	c, err := e.rel.AcceptInvitation(ctx, a, inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "pending" || c.OtherFamilyID != b.FamilyID {
		t.Fatalf("accepted connection %+v", c)
	}
	// Consumed invitations are no longer listed.
	if invs, _ := e.rel.ListInvitations(ctx, b); len(invs) != 0 {
		t.Fatalf("invitations after accept %+v", invs)
	}

	// Only the issuing family confirms.
	ca, cb := e.connections(t, a), e.connections(t, b)
	if len(ca) != 1 || ca[0].AwaitingUs || len(cb) != 1 || !cb[0].AwaitingUs || cb[0].OtherFamilyID != a.FamilyID {
		t.Fatalf("a=%+v b=%+v", ca, cb)
	}
	mustErr(t, e.rel.ConfirmConnection(ctx, a, c.ID), identity.ErrNotFound)
	if err := e.rel.ConfirmConnection(ctx, b, c.ID); err != nil {
		t.Fatal(err)
	}
	if cs := e.connections(t, a); cs[0].Status != "active" || cs[0].ConfirmedAt == nil {
		t.Fatalf("after confirm %+v", cs)
	}
	// Confirming twice is not possible.
	mustErr(t, e.rel.ConfirmConnection(ctx, b, c.ID), identity.ErrNotFound)

	// A third family sees nothing and cannot act.
	x := e.family(t)
	if cs := e.connections(t, x); len(cs) != 0 {
		t.Fatalf("outsider sees %+v", cs)
	}
	mustErr(t, e.rel.RevokeConnection(ctx, x, c.ID), identity.ErrNotFound)

	// Either family may revoke; revoking again is a no-op.
	if err := e.rel.RevokeConnection(ctx, a, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.rel.RevokeConnection(ctx, b, c.ID); err != nil {
		t.Fatal(err)
	}
	if cs := e.connections(t, b); cs[0].Status != "revoked" || cs[0].RevokedAt == nil {
		t.Fatalf("after revoke %+v", cs)
	}

	// Reconnecting goes through a new invitation and confirmation, here
	// issued by the other family.
	inv2, _ := e.rel.CreateInvitation(ctx, a)
	c2, err := e.rel.AcceptInvitation(ctx, b, inv2.Code)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ID != c.ID {
		t.Fatalf("reconnection created a new row %s, want %s", c2.ID, c.ID)
	}
	cb = e.connections(t, b)
	if cb[0].Status != "pending" || cb[0].AwaitingUs || cb[0].ConfirmedAt != nil || cb[0].RevokedAt != nil {
		t.Fatalf("reconnection %+v", cb)
	}
	mustErr(t, e.rel.ConfirmConnection(ctx, b, c.ID), identity.ErrNotFound)
	if err := e.rel.ConfirmConnection(ctx, a, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestInvitationCodes(t *testing.T) {
	e := newEnv(t)
	b := e.family(t)
	a := e.family(t)
	x := e.family(t)

	// Own invitation.
	inv, _ := e.rel.CreateInvitation(ctx, b)
	_, err := e.rel.AcceptInvitation(ctx, b, inv.Code)
	mustErr(t, err, identity.ErrInvalid)

	// Single use: a third family cannot reuse it.
	if _, err := e.rel.AcceptInvitation(ctx, a, inv.Code); err != nil {
		t.Fatal(err)
	}
	_, err = e.rel.AcceptInvitation(ctx, x, inv.Code)
	mustErr(t, err, identity.ErrInvalidCode)

	// Already connected (pending): conflict, and the new code stays usable.
	inv2, _ := e.rel.CreateInvitation(ctx, a)
	_, err = e.rel.AcceptInvitation(ctx, b, inv2.Code)
	mustErr(t, err, identity.ErrConflict)
	if _, err := e.rel.AcceptInvitation(ctx, x, inv2.Code); err != nil {
		t.Fatalf("code consumed by a failed accept: %v", err)
	}

	// Expiry.
	inv3, _ := e.rel.CreateInvitation(ctx, x)
	e.clk.advance(DefaultInvitationTTL)
	_, err = e.rel.AcceptInvitation(ctx, b, inv3.Code)
	mustErr(t, err, identity.ErrInvalidCode)

	// Revocation by the issuer only.
	inv4, _ := e.rel.CreateInvitation(ctx, x)
	mustErr(t, e.rel.RevokeInvitation(ctx, b, inv4.ID), identity.ErrNotFound)
	if err := e.rel.RevokeInvitation(ctx, x, inv4.ID); err != nil {
		t.Fatal(err)
	}
	mustErr(t, e.rel.RevokeInvitation(ctx, x, inv4.ID), identity.ErrNotFound)
	_, err = e.rel.AcceptInvitation(ctx, b, inv4.Code)
	mustErr(t, err, identity.ErrInvalidCode)

	// Garbage.
	_, err = e.rel.AcceptInvitation(ctx, b, "garbage")
	mustErr(t, err, identity.ErrInvalidCode)
	mustErr(t, e.rel.RevokeInvitation(ctx, x, "garbage"), identity.ErrNotFound)
}

func TestChildrenCannotManageConnections(t *testing.T) {
	e := newEnv(t)
	b := e.family(t)
	kid := e.child(t, b)
	_, err := e.rel.CreateInvitation(ctx, kid)
	mustErr(t, err, identity.ErrForbidden)
	_, err = e.rel.ListConnections(ctx, kid)
	mustErr(t, err, identity.ErrForbidden)
	inv, _ := e.rel.CreateInvitation(ctx, b)
	_, err = e.rel.AcceptInvitation(ctx, kid, inv.Code)
	mustErr(t, err, identity.ErrForbidden)
}

func TestConcurrentAccept(t *testing.T) {
	e := newEnv(t)
	b := e.family(t)
	inv, _ := e.rel.CreateInvitation(ctx, b)
	accepters := []identity.Principal{e.family(t), e.family(t), e.family(t), e.family(t)}

	errs := make(chan error, len(accepters))
	for _, a := range accepters {
		go func() {
			_, err := e.rel.AcceptInvitation(ctx, a, inv.Code)
			errs <- err
		}()
	}
	ok := 0
	for range accepters {
		err := <-errs
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, identity.ErrInvalidCode):
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d families accepted a single-use invitation", ok)
	}
}
