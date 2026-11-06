// Package httpapi exposes the backend HTTP API.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/frbayart/tkc/server/internal/identity"
	"github.com/frbayart/tkc/server/internal/relationship"
)

// API holds the HTTP handlers' dependencies.
type API struct {
	Identity      *identity.Service
	Relationships *relationship.Service
	Logger        *slog.Logger
}

// NewRouter returns the root HTTP handler.
func NewRouter(api *API) http.Handler {
	if api.Logger == nil {
		api.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	if api.Identity != nil {
		// Unauthenticated.
		mux.HandleFunc("POST /v1/families", api.createFamily)
		mux.HandleFunc("POST /v1/devices/enroll", api.enrollDevice)
		mux.HandleFunc("POST /v1/auth/challenge", api.challenge)
		mux.HandleFunc("POST /v1/auth/session", api.createSession)

		// Authenticated.
		mux.Handle("POST /v1/auth/logout", api.authed(api.logout))
		mux.Handle("GET /v1/family", api.authed(api.family))
		mux.Handle("POST /v1/users", api.authed(api.addUser))
		mux.Handle("POST /v1/users/{id}/enrollments", api.authed(api.createEnrollment))
		mux.Handle("POST /v1/devices/{id}/approve", api.authed(api.approveDevice))
		mux.Handle("POST /v1/devices/{id}/revoke", api.authed(api.revokeDevice))
	}

	if api.Identity != nil && api.Relationships != nil {
		mux.Handle("POST /v1/family-invitations", api.authed(api.createFamilyInvitation))
		mux.Handle("GET /v1/family-invitations", api.authed(api.listFamilyInvitations))
		mux.Handle("POST /v1/family-invitations/accept", api.authed(api.acceptFamilyInvitation))
		mux.Handle("POST /v1/family-invitations/{id}/revoke", api.authed(api.revokeFamilyInvitation))
		mux.Handle("GET /v1/family-connections", api.authed(api.listFamilyConnections))
		mux.Handle("POST /v1/family-connections/{id}/confirm", api.authed(api.confirmFamilyConnection))
		mux.Handle("POST /v1/family-connections/{id}/revoke", api.authed(api.revokeFamilyConnection))
	}
	return mux
}
