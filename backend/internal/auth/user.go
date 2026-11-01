// Package auth implements OIDC login, server-side sessions, personal API tokens and the
// middleware that puts the authenticated user into the request context.
package auth

import (
	"context"
	"time"
)

type User struct {
	ID          string
	Email       string
	DisplayName string
	Role        string
}

func (u User) IsAdmin() bool { return u.Role == "admin" }

// UserRecord is a user row as seen by login and administration code.
type UserRecord struct {
	User
	Claimed     bool // issuer/subject set; unclaimed users cannot log in
	Disabled    bool
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// Method records how a request authenticated. MethodNone is the zero value so that a
// missing method never reads as a session.
type Method int

const (
	MethodNone Method = iota
	MethodSession
	MethodToken
)

type ctxKey struct{}

type principal struct {
	user   User
	method Method
}

func WithUser(ctx context.Context, u User, m Method) context.Context {
	return context.WithValue(ctx, ctxKey{}, principal{user: u, method: m})
}

func UserFrom(ctx context.Context) (User, bool) {
	p, ok := ctx.Value(ctxKey{}).(principal)
	return p.user, ok
}

// MustUser panics when no user is present: RequireUser guarantees one, and Recoverer turns
// a wiring bug into a loud 500 instead of an unscoped query.
func MustUser(ctx context.Context) User {
	u, ok := UserFrom(ctx)
	if !ok {
		panic("auth: no user in context (route not behind RequireUser)")
	}
	return u
}

func MethodFrom(ctx context.Context) Method {
	p, _ := ctx.Value(ctxKey{}).(principal)
	return p.method
}
