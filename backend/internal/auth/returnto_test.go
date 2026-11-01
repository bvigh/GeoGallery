package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeReturnTo(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"/", "/"},
		{"/trips", "/trips"},
		{"/trips?id=5&x=a%2Fb", "/trips?id=5&x=a%2Fb"},
		{"/a/b/c", "/a/b/c"},
		{"/trips#frag", "/trips"},
		{"/caf%C3%A9", "/caf%C3%A9"},
		{"", "/"},
		{"trips", "/"},
		{"//evil.com", "/"},
		{"///evil.com", "/"},
		{"/\t/evil.com", "/"},
		{"/\n/evil.com", "/"},
		{"/\r/evil.com", "/"},
		{"/\x7f/evil.com", "/"},
		{"/%09/evil.com", "/"},
		{"/%0a/evil.com", "/"},
		{"/%2F/evil.com", "/"},
		{"/\\evil.com", "/"},
		{"\\\\evil.com", "/"},
		{"/%5Cevil.com", "/"},
		{"https://evil.com", "/"},
		{"http://evil.com/path", "/"},
		{"javascript:alert(1)", "/"},
		{"//user@evil.com", "/"},
		{"https:/evil.com", "/"},
		{"%2F%2Fevil.com", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			assert.Equal(t, tt.want, safeReturnTo(tt.raw))
		})
	}
}
