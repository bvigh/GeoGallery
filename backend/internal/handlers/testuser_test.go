package handlers

import (
	"net/http"

	"github.com/bvigh/geogallery/internal/auth"
)

const testOwnerID = "11111111-1111-4111-8111-111111111111"

// withTestUser authenticates r as testOwnerID. Handlers call auth.MustUser, which panics
// (and would crash the test binary) on a request that skipped this.
func withTestUser(r *http.Request) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), auth.User{ID: testOwnerID, Role: "user"}, auth.MethodSession))
}
