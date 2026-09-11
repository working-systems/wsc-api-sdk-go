package orgapi

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinels a caller branches on with errors.Is. Each is also matched by a
// *StatusError carrying the corresponding HTTP status, so the two styles of
// checking agree.
var (
	// ErrNotModified: the version sent as If-None-Match is still current (304).
	ErrNotModified = errors.New("orgapi: not modified")
	// ErrUnauthorized: no key, or an invalid or revoked one (401).
	ErrUnauthorized = errors.New("orgapi: unauthorized")
	// ErrForbidden: the key lacks the read scope (403).
	ErrForbidden = errors.New("orgapi: forbidden")
	// ErrRegistryReset: since is ahead of the registry's version (409). The
	// registry was restored or reset to an earlier point; nothing it could send
	// would patch the mirror correctly — drop it and load the full list.
	ErrRegistryReset = errors.New("orgapi: since is ahead of the registry version (restored or reset)")
	// ErrBadRequest: the registry rejected the request itself (422): since
	// combined with status, a negative since, an unknown status.
	ErrBadRequest = errors.New("orgapi: request rejected")
	// ErrSchema: the payload's schema tag is not the one this client speaks.
	// Nothing about it can be trusted, so nothing is returned.
	ErrSchema = errors.New("orgapi: unsupported list schema")
)

// StatusError is any non-2xx answer other than 304, with what the registry's
// RFC-7807 body said when it said anything.
type StatusError struct {
	Status int
	Method string
	URL    string
	Title  string
	Detail string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("orgapi: %s %s: %d %s", e.Method, e.URL, e.Status, http.StatusText(e.Status))
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Is maps the status to the package sentinels so errors.Is(err, ErrForbidden)
// works whether the caller holds the sentinel or the *StatusError.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrNotModified:
		return e.Status == http.StatusNotModified
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	case ErrRegistryReset:
		return e.Status == http.StatusConflict
	case ErrBadRequest:
		return e.Status == http.StatusUnprocessableEntity || e.Status == http.StatusBadRequest
	}
	return false
}
