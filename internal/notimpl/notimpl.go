// Package notimpl provides the sentinel error returned by every stub that has
// not been implemented yet.
package notimpl

import (
	"errors"
	"fmt"
)

// ErrNotImplemented is the sentinel wrapped by Err. Match it with errors.Is.
var ErrNotImplemented = errors.New("not implemented")

// Err returns an error naming the unimplemented function, e.g. "backend.StateBackend.Ls".
func Err(where string) error {
	return fmt.Errorf("%s: %w", where, ErrNotImplemented)
}
