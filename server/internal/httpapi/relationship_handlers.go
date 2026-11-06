package httpapi

import (
	"net/http"

	"github.com/frbayart/tkc/server/internal/policy"
	"github.com/frbayart/tkc/server/internal/relationship"
)

func (a *API) createExchangeCode(w http.ResponseWriter, r *http.Request) {
	ec, err := a.Relationships.CreateExchangeCode(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ec)
}

func (a *API) redeemExchangeCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	rel, err := a.Relationships.RedeemExchangeCode(r.Context(), principal(r), req.Code)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rel)
}

func (a *API) listRelationships(w http.ResponseWriter, r *http.Request) {
	rels, err := a.Relationships.List(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Relationships []relationship.Relationship `json:"relationships"`
	}{nonNil(rels)})
}

func (a *API) getRelationship(w http.ResponseWriter, r *http.Request) {
	d, err := a.Relationships.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type policyRequest struct {
	Policy policy.FamilyPolicy `json:"policy"`
}

func (a *API) approveRelationship(w http.ResponseWriter, r *http.Request) {
	var req policyRequest
	if !decode(w, r, &req) {
		return
	}
	rel, err := a.Relationships.Approve(r.Context(), principal(r), r.PathValue("id"), req.Policy)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

func (a *API) updateRelationshipPolicy(w http.ResponseWriter, r *http.Request) {
	var req policyRequest
	if !decode(w, r, &req) {
		return
	}
	if err := a.Relationships.UpdatePolicy(r.Context(), principal(r), r.PathValue("id"), req.Policy); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// noContent adapts a service call to a handler answering 204.
func (a *API) noContent(f func(r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f(r); err != nil {
			a.writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) suspendRelationship(r *http.Request) error {
	return a.Relationships.Suspend(r.Context(), principal(r), r.PathValue("id"))
}

func (a *API) resumeRelationship(r *http.Request) error {
	return a.Relationships.Resume(r.Context(), principal(r), r.PathValue("id"))
}

func (a *API) revokeRelationship(r *http.Request) error {
	return a.Relationships.Revoke(r.Context(), principal(r), r.PathValue("id"))
}

func (a *API) closeProposal(r *http.Request) error {
	return a.Relationships.CloseProposal(r.Context(), principal(r), r.PathValue("id"))
}

type childPolicyRequest struct {
	ChildID string              `json:"child_id"`
	Policy  policy.FamilyPolicy `json:"policy"`
}

func (a *API) proposeRelationship(w http.ResponseWriter, r *http.Request) {
	var req childPolicyRequest
	if !decode(w, r, &req) {
		return
	}
	pr, err := a.Relationships.Propose(r.Context(), principal(r), r.PathValue("id"), req.ChildID, req.Policy)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pr)
}

func (a *API) listProposals(w http.ResponseWriter, r *http.Request) {
	ps, err := a.Relationships.ListProposals(r.Context(), principal(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Proposals []relationship.Proposal `json:"proposals"`
	}{nonNil(ps)})
}

func (a *API) acceptProposal(w http.ResponseWriter, r *http.Request) {
	var req childPolicyRequest
	if !decode(w, r, &req) {
		return
	}
	rel, err := a.Relationships.AcceptProposal(r.Context(), principal(r), r.PathValue("id"), req.ChildID, req.Policy)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rel)
}
