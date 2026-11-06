package httpapi

import (
	"testing"

	"github.com/frbayart/tkc/server/internal/relationship"
)

// newParent creates a family over HTTP and returns the parent's token and
// family ID.
func (c *client) newParent() (string, string) {
	c.t.Helper()
	d := newTestDevice(c.t)
	var fam principalJSON
	c.expect(201, "POST", "/v1/families", "", map[string]any{"time_zone": "Europe/Paris", "device": d.json}, &fam)
	return c.login(fam.DeviceID, d), fam.FamilyID
}

func TestFamilyConnectionHTTP(t *testing.T) {
	c := newClient(t)
	tokenB, familyB := c.newParent()
	tokenA, familyA := c.newParent()

	var inv relationship.Invitation
	c.expect(201, "POST", "/v1/family-invitations", tokenB, nil, &inv)
	if inv.Code == "" || inv.ID == "" {
		t.Fatalf("invitation %+v", inv)
	}
	var invs struct{ Invitations []relationship.Invitation }
	c.expect(200, "GET", "/v1/family-invitations", tokenB, nil, &invs)
	if len(invs.Invitations) != 1 || invs.Invitations[0].Code != "" {
		t.Fatalf("listed invitations %+v (codes must not be listed)", invs)
	}

	var conn relationship.Connection
	c.expect(201, "POST", "/v1/family-invitations/accept", tokenA, map[string]string{"code": inv.Code}, &conn)
	if conn.Status != "pending" || conn.OtherFamilyID != familyB {
		t.Fatalf("connection %+v", conn)
	}
	c.expect(404, "POST", "/v1/family-invitations/accept", tokenA, map[string]string{"code": inv.Code}, nil)

	c.expect(404, "POST", "/v1/family-connections/"+conn.ID+"/confirm", tokenA, nil, nil)
	c.expect(204, "POST", "/v1/family-connections/"+conn.ID+"/confirm", tokenB, nil, nil)

	var list struct{ Connections []relationship.Connection }
	c.expect(200, "GET", "/v1/family-connections", tokenA, nil, &list)
	if len(list.Connections) != 1 || list.Connections[0].Status != "active" || list.Connections[0].OtherFamilyID != familyB {
		t.Fatalf("connections of A %+v", list)
	}
	c.expect(200, "GET", "/v1/family-connections", tokenB, nil, &list)
	if list.Connections[0].OtherFamilyID != familyA {
		t.Fatalf("connections of B %+v", list)
	}

	c.expect(204, "POST", "/v1/family-connections/"+conn.ID+"/revoke", tokenA, nil, nil)

	// Unused invitations can be revoked by their issuer.
	var inv2 relationship.Invitation
	c.expect(201, "POST", "/v1/family-invitations", tokenB, nil, &inv2)
	c.expect(404, "POST", "/v1/family-invitations/"+inv2.ID+"/revoke", tokenA, nil, nil)
	c.expect(204, "POST", "/v1/family-invitations/"+inv2.ID+"/revoke", tokenB, nil, nil)
	c.expect(404, "POST", "/v1/family-invitations/accept", tokenA, map[string]string{"code": inv2.Code}, nil)

	// Empty lists are arrays, not null.
	tokenX, _ := c.newParent()
	var empty struct {
		Invitations *[]relationship.Invitation
	}
	c.expect(200, "GET", "/v1/family-invitations", tokenX, nil, &empty)
	if empty.Invitations == nil {
		t.Fatal("invitations is null")
	}

	c.expect(401, "POST", "/v1/family-invitations", "", nil, nil)
}
