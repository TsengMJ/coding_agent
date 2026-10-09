// Package auth identifies the users of the remote service: GitHub device
// flow login, session tokens, and the user carried by a request's context.
package auth

import "context"

// User is the signed-in user of a request.
type User struct {
	// ID is the GitHub login; it owns threads and workspaces.
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// GitHubToken clones private repositories and opens pull requests.
	GitHubToken string `json:"-"`
}

type userKey struct{}

// WithUser returns ctx carrying u.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the request's user; ok is false without login (the
// local single-user server).
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey{}).(User)
	return u, ok
}

// OwnerFrom is the owner ID for records created by the request; empty
// without login.
func OwnerFrom(ctx context.Context) string {
	u, _ := UserFrom(ctx)
	return u.ID
}
