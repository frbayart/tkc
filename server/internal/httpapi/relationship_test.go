package httpapi

import (
	"testing"

	"github.com/frbayart/tkc/server/internal/relationship"
)

// newChild adds a child to the parent's family, enrolls and approves a
// device for it, and returns the child's user ID and token.
func (c *client) newChild(parentToken string) (string, string) {
	c.t.Helper()
	var u struct {
		UserID string `json:"user_id"`
	}
	c.expect(201, "POST", "/v1/users", parentToken, map[string]string{"role": "child", "time_zone": "Europe/Paris"}, &u)
	var enr struct{ Code string }
	c.expect(201, "POST", "/v1/users/"+u.UserID+"/enrollments", parentToken, nil, &enr)
	d := newTestDevice(c.t)
	var dev struct {
		DeviceID string `json:"device_id"`
	}
	c.expect(201, "POST", "/v1/devices/enroll", "", map[string]any{"code": enr.Code, "device": d.json}, &dev)
	c.expect(204, "POST", "/v1/devices/"+dev.DeviceID+"/approve", parentToken,
		map[string]any{"identity_public_key": d.json.IdentityPublicKey}, nil)
	return u.UserID, c.login(dev.DeviceID, d)
}

func policyJSON(video bool, maxMinutes int) map[string]any {
	return map[string]any{"policy": map[string]any{
		"messaging":        map[string]any{"text": true},
		"audio":            map[string]any{"enabled": true},
		"video":            map[string]any{"enabled": video},
		"call_schedule":    map[string]any{"windows": []any{map[string]any{"days": []int{0, 1, 2, 3, 4, 5, 6}, "from": "08:00", "until": "20:00"}}},
		"max_call_minutes": maxMinutes,
	}}
}

func TestRelationshipsHTTP(t *testing.T) {
	c := newClient(t)
	parentA, _ := c.newParent()
	parentB, _ := c.newParent()
	kidA, tokenKidA := c.newChild(parentA)
	_, tokenKidB := c.newChild(parentB)

	// In-person exchange.
	c.expect(403, "POST", "/v1/exchange-codes", parentA, nil, nil)
	var ec relationship.ExchangeCode
	c.expect(201, "POST", "/v1/exchange-codes", tokenKidA, nil, &ec)
	var rel relationship.Relationship
	c.expect(201, "POST", "/v1/exchange-codes/redeem", tokenKidB, map[string]string{"code": ec.Code}, &rel)
	if rel.Status != "proposed" || rel.OtherChildID != kidA {
		t.Fatalf("redeemed %+v", rel)
	}

	// Both parents approve with their policies.
	c.expect(400, "POST", "/v1/relationships/"+rel.ID+"/approve", parentA, map[string]any{"policy": map[string]any{"bogus": 1}}, nil)
	c.expect(200, "POST", "/v1/relationships/"+rel.ID+"/approve", parentA, policyJSON(true, 30), &rel)
	if rel.Status != "pending" {
		t.Fatalf("after A %+v", rel)
	}
	c.expect(200, "POST", "/v1/relationships/"+rel.ID+"/approve", parentB, policyJSON(false, 20), &rel)
	if rel.Status != "active" {
		t.Fatalf("after B %+v", rel)
	}

	var d relationship.Details
	c.expect(200, "GET", "/v1/relationships/"+rel.ID, tokenKidA, nil, &d)
	if d.Effective == nil || d.Effective.Video || d.Effective.MaxCallMinutes != 20 || d.OurPolicy != nil {
		t.Fatalf("child view %+v", d)
	}
	c.expect(200, "GET", "/v1/relationships/"+rel.ID, parentA, nil, &d)
	if d.OurPolicy == nil || d.OurPolicy.TimeZone != "Europe/Paris" {
		t.Fatalf("parent view %+v", d)
	}

	c.expect(204, "PUT", "/v1/relationships/"+rel.ID+"/policy", parentA, policyJSON(true, 10), nil)
	c.expect(204, "POST", "/v1/relationships/"+rel.ID+"/suspend", parentA, nil, nil)
	c.expect(409, "POST", "/v1/relationships/"+rel.ID+"/resume", parentB, nil, nil)
	c.expect(204, "POST", "/v1/relationships/"+rel.ID+"/resume", parentA, nil, nil)

	var list struct{ Relationships []relationship.Relationship }
	c.expect(200, "GET", "/v1/relationships", tokenKidB, nil, &list)
	if len(list.Relationships) != 1 {
		t.Fatalf("child list %+v", list)
	}

	// A child blocks it.
	c.expect(204, "POST", "/v1/relationships/"+rel.ID+"/revoke", tokenKidB, nil, nil)
	c.expect(200, "GET", "/v1/relationships", tokenKidB, nil, &list)
	if len(list.Relationships) != 0 {
		t.Fatalf("child list after block %+v", list)
	}
}

func TestProposalsHTTP(t *testing.T) {
	c := newClient(t)
	parentA, _ := c.newParent()
	parentB, _ := c.newParent()
	kidA, _ := c.newChild(parentA)
	kidB, _ := c.newChild(parentB)

	var inv relationship.Invitation
	c.expect(201, "POST", "/v1/family-invitations", parentA, nil, &inv)
	var conn relationship.Connection
	c.expect(201, "POST", "/v1/family-invitations/accept", parentB, map[string]string{"code": inv.Code}, &conn)
	c.expect(204, "POST", "/v1/family-connections/"+conn.ID+"/confirm", parentA, nil, nil)

	body := policyJSON(true, 30)
	body["child_id"] = kidA
	var pr relationship.Proposal
	c.expect(201, "POST", "/v1/family-connections/"+conn.ID+"/proposals", parentA, body, &pr)

	var ps struct{ Proposals []relationship.Proposal }
	c.expect(200, "GET", "/v1/relationship-proposals", parentB, nil, &ps)
	if len(ps.Proposals) != 1 || !ps.Proposals[0].Incoming || ps.Proposals[0].OurChildID != "" {
		t.Fatalf("incoming %+v", ps)
	}

	accept := policyJSON(false, 20)
	accept["child_id"] = kidB
	var rel relationship.Relationship
	c.expect(201, "POST", "/v1/relationship-proposals/"+pr.ID+"/accept", parentB, accept, &rel)
	if rel.Status != "active" || rel.OtherChildID != kidA {
		t.Fatalf("accepted %+v", rel)
	}

	// Revoking the family connection revokes the relationship.
	c.expect(204, "POST", "/v1/family-connections/"+conn.ID+"/revoke", parentA, nil, nil)
	var d relationship.Details
	c.expect(200, "GET", "/v1/relationships/"+rel.ID, parentB, nil, &d)
	if d.Status != "revoked" {
		t.Fatalf("after connection revocation %+v", d)
	}

	// Closing a proposal.
	var inv2 relationship.Invitation
	c.expect(201, "POST", "/v1/family-invitations", parentA, nil, &inv2)
	c.expect(201, "POST", "/v1/family-invitations/accept", parentB, map[string]string{"code": inv2.Code}, &conn)
	c.expect(204, "POST", "/v1/family-connections/"+conn.ID+"/confirm", parentA, nil, nil)
	c.expect(201, "POST", "/v1/family-connections/"+conn.ID+"/proposals", parentA, body, &pr)
	c.expect(204, "POST", "/v1/relationship-proposals/"+pr.ID+"/close", parentB, nil, nil)
	c.expect(404, "POST", "/v1/relationship-proposals/"+pr.ID+"/close", parentA, nil, nil)
}

func TestRemoveMemberHTTP(t *testing.T) {
	c := newClient(t)
	parent, _ := c.newParent()
	kid, kidToken := c.newChild(parent)
	c.expect(403, "DELETE", "/v1/users/"+kid, kidToken, nil, nil)
	c.expect(204, "DELETE", "/v1/users/"+kid, parent, nil, nil)
	c.expect(401, "GET", "/v1/family", kidToken, nil, nil)
	c.expect(404, "DELETE", "/v1/users/"+kid, parent, nil, nil)
}

func TestChildBlockNotificationHTTP(t *testing.T) {
	c := newClient(t)
	parentA, _ := c.newParent()
	parentB, _ := c.newParent()
	_, tokenKidA := c.newChild(parentA)
	_, tokenKidB := c.newChild(parentB)

	var ec relationship.ExchangeCode
	c.expect(201, "POST", "/v1/exchange-codes", tokenKidA, nil, &ec)
	var rel relationship.Relationship
	c.expect(201, "POST", "/v1/exchange-codes/redeem", tokenKidB, map[string]string{"code": ec.Code}, &rel)
	c.expect(204, "POST", "/v1/relationships/"+rel.ID+"/revoke", tokenKidB, nil, nil)

	c.expect(403, "GET", "/v1/notifications", tokenKidB, nil, nil)
	var ns struct{ Notifications []relationship.Notification }
	c.expect(200, "GET", "/v1/notifications", parentB, nil, &ns)
	if len(ns.Notifications) != 1 || ns.Notifications[0].Kind != "relationship_blocked_by_child" {
		t.Fatalf("notifications %+v", ns)
	}
	c.expect(404, "POST", "/v1/notifications/"+ns.Notifications[0].ID+"/read", parentA, nil, nil)
	c.expect(204, "POST", "/v1/notifications/"+ns.Notifications[0].ID+"/read", parentB, nil, nil)
}
