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
		mux.Handle("DELETE /v1/users/{id}", api.authed(api.removeUser))
		mux.Handle("POST /v1/users/{id}/profiles", api.authed(api.createProfile))
		mux.Handle("GET /v1/users/{id}/profiles", api.authed(api.profileHistory))
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
		mux.Handle("POST /v1/family-connections/{id}/proposals", api.authed(api.proposeRelationship))

		mux.Handle("GET /v1/relationship-proposals", api.authed(api.listProposals))
		mux.Handle("POST /v1/relationship-proposals/{id}/accept", api.authed(api.acceptProposal))
		mux.Handle("POST /v1/relationship-proposals/{id}/close", api.authed(api.noContent(api.closeProposal)))

		mux.Handle("POST /v1/exchange-codes", api.authed(api.createExchangeCode))
		mux.Handle("POST /v1/exchange-codes/redeem", api.authed(api.redeemExchangeCode))

		mux.Handle("GET /v1/relationships", api.authed(api.listRelationships))
		mux.Handle("GET /v1/relationships/{id}", api.authed(api.getRelationship))
		mux.Handle("POST /v1/relationships/{id}/approve", api.authed(api.approveRelationship))
		mux.Handle("PUT /v1/relationships/{id}/policy", api.authed(api.updateRelationshipPolicy))
		mux.Handle("POST /v1/relationships/{id}/suspend", api.authed(api.noContent(api.suspendRelationship)))
		mux.Handle("POST /v1/relationships/{id}/resume", api.authed(api.noContent(api.resumeRelationship)))
		mux.Handle("POST /v1/relationships/{id}/revoke", api.authed(api.noContent(api.revokeRelationship)))
		mux.Handle("POST /v1/relationships/{id}/hold", api.authed(api.setHold))
		mux.Handle("DELETE /v1/relationships/{id}/hold", api.authed(api.noContent(api.releaseHold)))

		mux.Handle("GET /v1/notifications", api.authed(api.listNotifications))
		mux.Handle("POST /v1/notifications/{id}/read", api.authed(api.noContent(api.markNotificationRead)))
	}
	return mux
}
