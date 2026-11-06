package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/frbayart/tkc/server/internal/identity"
)

const maxBodyBytes = 64 << 10

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a JSON body strictly: bounded size, no unknown fields, a
// single value.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody{"invalid_request"})
		return false
	}
	return true
}

// writeError maps service errors to HTTP responses without leaking details.
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var status int
	var code string
	switch {
	case errors.Is(err, identity.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, identity.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, "unauthenticated"
	case errors.Is(err, identity.ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, identity.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, identity.ErrInvalidCode):
		status, code = http.StatusNotFound, "invalid_code"
	case errors.Is(err, identity.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, identity.ErrKeyMismatch):
		status, code = http.StatusConflict, "key_mismatch"
	case errors.Is(err, identity.ErrLastParentDevice):
		status, code = http.StatusConflict, "last_parent_device"
	default:
		a.Logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		status, code = http.StatusInternalServerError, "internal_error"
	}
	writeJSON(w, status, errorBody{code})
}

type principalKey struct{}

func principal(r *http.Request) identity.Principal {
	return r.Context().Value(principalKey{}).(identity.Principal)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}

// authed wraps a handler with session authentication.
func (a *API) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			a.writeError(w, r, identity.ErrUnauthenticated)
			return
		}
		p, err := a.Identity.Authenticate(r.Context(), token)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
