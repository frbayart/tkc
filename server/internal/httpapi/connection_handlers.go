package httpapi

import (
	"net/http"

	"github.com/frbayart/tkc/server/internal/relationship"
)

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (a *API) createFamilyInvitation(w http.ResponseWriter, r *http.Request) {
	inv, err := a.Relationships.CreateInvitation(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (a *API) listFamilyInvitations(w http.ResponseWriter, r *http.Request) {
	invs, err := a.Relationships.ListInvitations(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Invitations []relationship.Invitation `json:"invitations"`
	}{nonNil(invs)})
}

func (a *API) revokeFamilyInvitation(w http.ResponseWriter, r *http.Request) {
	if err := a.Relationships.RevokeInvitation(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) acceptFamilyInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := a.Relationships.AcceptInvitation(r.Context(), principal(r), req.Code)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *API) listFamilyConnections(w http.ResponseWriter, r *http.Request) {
	cs, err := a.Relationships.ListConnections(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Connections []relationship.Connection `json:"connections"`
	}{nonNil(cs)})
}

func (a *API) confirmFamilyConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.Relationships.ConfirmConnection(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) revokeFamilyConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.Relationships.RevokeConnection(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
