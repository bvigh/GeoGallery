package auth

import (
	"net/url"
	"strings"
)

// safeReturnTo accepts only same-origin absolute paths; anything else yields "/".
// Control bytes and backslashes are rejected before parsing because browsers strip tabs and
// newlines and treat "\" as "/", which turns "/\t/evil.com" or "/\evil.com" into "//evil.com".
func safeReturnTo(raw string) string {
	if hasUnsafeByte(raw) {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	if !isSafePath(u.Path) || !isSafePath(u.EscapedPath()) || hasUnsafeByte(u.Path) {
		return "/"
	}
	return u.RequestURI()
}

func isSafePath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//")
}

func hasUnsafeByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return true
		}
	}
	return false
}
