package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/jackc/pgx/v5/pgtype"
)

const apiTokenPrefix = "gg_pat_"

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// newToken returns a session/invite token and the sha256 hash that is stored instead of it.
func newToken() (raw string, hash []byte) {
	raw = randomString()
	return raw, hashToken(raw)
}

func newAPIToken() (raw string, hash []byte, displayHint string) {
	secret := randomString()
	raw = apiTokenPrefix + secret
	return raw, hashToken(raw), apiTokenPrefix + secret[:6]
}

// CanonicalUUID returns the canonical form of s, or false if s is not a UUID. Validating
// before querying turns a Postgres 22P02 (a 500) into a clean 404.
func CanonicalUUID(s string) (string, bool) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil || !u.Valid {
		return "", false
	}
	v, err := u.Value()
	if err != nil {
		return "", false
	}
	str, ok := v.(string)
	return str, ok
}
