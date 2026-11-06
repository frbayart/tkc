package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frbayart/tkc/server/internal/identity"
	"github.com/frbayart/tkc/server/internal/relationship"
	"github.com/frbayart/tkc/server/internal/store/storetest"
)

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func newClient(t *testing.T) *client {
	t.Helper()
	pool := storetest.Pool(t)
	api := &API{
		Identity:      identity.New(pool),
		Relationships: relationship.New(pool),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(NewRouter(api))
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv}
}

// do sends a JSON request and decodes the JSON response into out (if any).
func (c *client) do(method, path, token string, body, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.srv.URL+path, r)
	if err != nil {
		c.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func (c *client) expect(want int, method, path, token string, body, out any) {
	c.t.Helper()
	if got := c.do(method, path, token, body, out); got != want {
		c.t.Fatalf("%s %s: status %d, want %d", method, path, got, want)
	}
}

type testDevice struct {
	priv ed25519.PrivateKey
	json deviceKeysJSON
}

func newTestDevice(t *testing.T) testDevice {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc := append([]byte("enc:"), pub[:8]...)
	return testDevice{priv, deviceKeysJSON{
		DeviceType: "tablet", Platform: "ios",
		IdentityPublicKey: pub, EncryptionPublicKey: enc,
		Proof: ed25519.Sign(priv, identity.ProofMessage(enc)),
	}}
}

func (c *client) login(deviceID string, d testDevice) string {
	c.t.Helper()
	var ch struct{ Nonce []byte }
	c.expect(200, "POST", "/v1/auth/challenge", "", map[string]string{"device_id": deviceID}, &ch)
	msg, err := identity.ChallengeMessage(deviceID, ch.Nonce)
	if err != nil {
		c.t.Fatal(err)
	}
	var s struct{ Token string }
	c.expect(201, "POST", "/v1/auth/session", "", map[string]any{
		"device_id": deviceID, "nonce": ch.Nonce, "signature": ed25519.Sign(d.priv, msg),
	}, &s)
	return s.Token
}

func TestIdentityFlowHTTP(t *testing.T) {
	c := newClient(t)

	// Parent creates the family.
	pd := newTestDevice(t)
	var fam principalJSON
	c.expect(201, "POST", "/v1/families", "", map[string]any{"time_zone": "Europe/Paris", "device": pd.json}, &fam)
	parentToken := c.login(fam.DeviceID, pd)

	// Adds a child and an enrollment code for them.
	var child struct {
		UserID string `json:"user_id"`
	}
	c.expect(201, "POST", "/v1/users", parentToken, map[string]string{"role": "child", "time_zone": "Europe/Paris"}, &child)
	var enr struct{ Code string }
	c.expect(201, "POST", "/v1/users/"+child.UserID+"/enrollments", parentToken, nil, &enr)

	// The child's tablet enrolls and waits for approval.
	cd := newTestDevice(t)
	var enrolled struct {
		DeviceID string `json:"device_id"`
		Status   string
	}
	c.expect(201, "POST", "/v1/devices/enroll", "", map[string]any{"code": enr.Code, "device": cd.json}, &enrolled)
	if enrolled.Status != "pending" {
		t.Fatalf("status %q", enrolled.Status)
	}
	// Code is single use.
	c.expect(404, "POST", "/v1/devices/enroll", "", map[string]any{"code": enr.Code, "device": newTestDevice(t).json}, nil)

	// The parent must approve the key it displayed.
	c.expect(409, "POST", "/v1/devices/"+enrolled.DeviceID+"/approve", parentToken,
		map[string]any{"identity_public_key": newTestDevice(t).json.IdentityPublicKey}, nil)
	c.expect(204, "POST", "/v1/devices/"+enrolled.DeviceID+"/approve", parentToken,
		map[string]any{"identity_public_key": cd.json.IdentityPublicKey}, nil)
	childToken := c.login(enrolled.DeviceID, cd)

	// Both see the family with all device keys.
	var f identity.Family
	c.expect(200, "GET", "/v1/family", childToken, nil, &f)
	if len(f.Members) != 2 || len(f.Members[1].Devices) != 1 ||
		!bytes.Equal(f.Members[1].Devices[0].IdentityPublicKey, cd.json.IdentityPublicKey) {
		t.Fatalf("family %+v", f)
	}

	// A child cannot add users.
	c.expect(403, "POST", "/v1/users", childToken, map[string]string{"role": "child", "time_zone": "UTC"}, nil)

	// The parent revokes the child's device: its token stops working.
	c.expect(204, "POST", "/v1/devices/"+enrolled.DeviceID+"/revoke", parentToken, nil, nil)
	c.expect(401, "GET", "/v1/family", childToken, nil, nil)

	// The last parent device cannot be revoked.
	c.expect(409, "POST", "/v1/devices/"+fam.DeviceID+"/revoke", parentToken, nil, nil)

	// Logout.
	c.expect(204, "POST", "/v1/auth/logout", parentToken, nil, nil)
	c.expect(401, "GET", "/v1/family", parentToken, nil, nil)
}

func TestHTTPInputValidation(t *testing.T) {
	c := newClient(t)
	c.expect(401, "GET", "/v1/family", "", nil, nil)
	c.expect(401, "GET", "/v1/family", "nope", nil, nil)
	c.expect(400, "POST", "/v1/families", "", map[string]any{"unknown": true}, nil)
	c.expect(400, "POST", "/v1/families", "", map[string]any{"time_zone": "UTC", "device": newTestDevice(t).json, "extra": 1}, nil)
	bad := newTestDevice(t)
	bad.json.Proof = make([]byte, 64)
	c.expect(400, "POST", "/v1/families", "", map[string]any{"time_zone": "UTC", "device": bad.json}, nil)
	c.expect(400, "POST", "/v1/auth/challenge", "", map[string]string{"device_id": "not-a-uuid"}, nil)
	c.expect(401, "POST", "/v1/auth/session", "", map[string]any{"device_id": "not-a-uuid"}, nil)

	d := newTestDevice(t)
	var fam principalJSON
	c.expect(201, "POST", "/v1/families", "", map[string]any{"time_zone": "UTC", "device": d.json}, &fam)
	c.expect(409, "POST", "/v1/families", "", map[string]any{"time_zone": "UTC", "device": d.json}, nil)
	token := c.login(fam.DeviceID, d)
	c.expect(404, "POST", "/v1/users/not-a-uuid/enrollments", token, nil, nil)
	c.expect(404, "POST", "/v1/devices/00000000-0000-4000-8000-000000000000/revoke", token, nil, nil)
}
