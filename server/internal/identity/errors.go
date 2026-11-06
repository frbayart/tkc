package identity

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Errors returned by the service. The HTTP layer maps them to status codes.
var (
	ErrInvalid          = errors.New("invalid request")
	ErrUnauthenticated  = errors.New("unauthenticated")
	ErrForbidden        = errors.New("forbidden")
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("conflict")
	ErrInvalidCode      = errors.New("invalid or expired code")
	ErrKeyMismatch      = errors.New("device key does not match")
	ErrLastParentDevice = errors.New("cannot revoke the last active parent device")
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
