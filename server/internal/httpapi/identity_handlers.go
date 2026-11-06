package httpapi

import (
	"net/http"
	"time"

	"github.com/frbayart/tkc/server/internal/identity"
)

// Binary values (keys, signatures, nonces) are standard base64 in JSON.

type deviceKeysJSON struct {
	DeviceType          string `json:"device_type"`
	Platform            string `json:"platform"`
	IdentityPublicKey   []byte `json:"identity_public_key"`
	EncryptionPublicKey []byte `json:"encryption_public_key"`
	Proof               []byte `json:"proof"`
}

func (k deviceKeysJSON) keys() identity.DeviceKeys {
	return identity.DeviceKeys(k)
}

type principalJSON struct {
	FamilyID string `json:"family_id"`
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id"`
}

func (a *API) createFamily(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TimeZone string         `json:"time_zone"`
		Device   deviceKeysJSON `json:"device"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, err := a.Identity.CreateFamily(r.Context(), req.TimeZone, req.Device.keys())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, principalJSON{p.FamilyID, p.UserID, p.DeviceID})
}

func (a *API) enrollDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string         `json:"code"`
		Device deviceKeysJSON `json:"device"`
	}
	if !decode(w, r, &req) {
		return
	}
	deviceID, userID, err := a.Identity.Enroll(r.Context(), req.Code, req.Device.keys())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		DeviceID string `json:"device_id"`
		UserID   string `json:"user_id"`
		Status   string `json:"status"`
	}{deviceID, userID, "pending"})
}

func (a *API) challenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	nonce, expires, err := a.Identity.Challenge(r.Context(), req.DeviceID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Nonce     []byte    `json:"nonce"`
		ExpiresAt time.Time `json:"expires_at"`
	}{nonce, expires})
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID  string `json:"device_id"`
		Nonce     []byte `json:"nonce"`
		Signature []byte `json:"signature"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, expires, err := a.Identity.CreateSession(r.Context(), req.DeviceID, req.Nonce, req.Signature)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}{token, expires})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.Identity.Logout(r.Context(), bearerToken(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) family(w http.ResponseWriter, r *http.Request) {
	f, err := a.Identity.Family(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *API) addUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role     identity.Role `json:"role"`
		TimeZone string        `json:"time_zone"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, err := a.Identity.AddUser(r.Context(), principal(r), req.Role, req.TimeZone)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		UserID string `json:"user_id"`
	}{id})
}

func (a *API) createEnrollment(w http.ResponseWriter, r *http.Request) {
	code, expires, err := a.Identity.CreateEnrollment(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}{code, expires})
}

func (a *API) approveDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IdentityPublicKey []byte `json:"identity_public_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := a.Identity.ApproveDevice(r.Context(), principal(r), r.PathValue("id"), req.IdentityPublicKey); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if err := a.Identity.RevokeDevice(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) removeUser(w http.ResponseWriter, r *http.Request) {
	if err := a.Identity.RemoveUser(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
