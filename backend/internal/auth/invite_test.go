package auth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inviteTokenFromURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/api/auth/login", u.Path)
	return u.Query().Get("invite")
}

func TestIssueCLIInvite(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(s *fakeStore) (wantClaim string)
		admin     bool
		wantRole  string
		wantClaim bool
	}{
		{
			name:     "user invite",
			seed:     func(s *fakeStore) string { s.addUser("", "", "", "admin", false); return "" },
			wantRole: "user",
		},
		{
			name: "admin claims unclaimed admin",
			seed: func(s *fakeStore) string {
				s.addUser("iss", "x", "", "admin", false)
				return s.addUser("", "", "", "admin", false).ID
			},
			admin: true, wantRole: "admin", wantClaim: true,
		},
		{
			name: "admin skips unclaimed non-admin and disabled admin",
			seed: func(s *fakeStore) string {
				s.addUser("", "", "", "user", false)
				s.addUser("", "", "", "admin", true)
				return ""
			},
			admin: true, wantRole: "admin",
		},
		{
			name:     "admin with everyone claimed creates plain admin invite",
			seed:     func(s *fakeStore) string { s.addUser("iss", "x", "", "admin", false); return "" },
			admin:    true,
			wantRole: "admin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeStore()
			wantClaim := tt.seed(s)
			got, claim, err := IssueCLIInvite(context.Background(), s, "http://localhost:5173", tt.admin, 72*time.Hour)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(got, "http://localhost:5173/api/auth/login?invite="))
			assert.Equal(t, wantClaim, claim)
			inv := s.invites[string(hashToken(inviteTokenFromURL(t, got)))]
			require.NotNil(t, inv)
			assert.Equal(t, tt.wantRole, inv.Role)
			assert.Equal(t, wantClaim, inv.ClaimUserID)
			assert.Empty(t, inv.CreatedBy)
			assert.WithinDuration(t, time.Now().Add(72*time.Hour), inv.ExpiresAt, time.Minute)
		})
	}
}

func TestIssueCLIInvite_PrefersLegacyOwnerAndRevokesEarlierClaim(t *testing.T) {
	s := newFakeStore()
	s.addUser("", "", "", "admin", false)
	s.users[LegacyOwnerID] = &fakeUser{UserRecord: UserRecord{User: User{ID: LegacyOwnerID, Role: "admin"}}}

	first, claim, err := IssueCLIInvite(context.Background(), s, "http://o", true, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, LegacyOwnerID, claim)
	second, _, err := IssueCLIInvite(context.Background(), s, "http://o", true, time.Hour)
	require.NoError(t, err)

	assert.NotNil(t, s.invites[string(hashToken(inviteTokenFromURL(t, first)))].usedAt, "earlier claim invite revoked")
	assert.Nil(t, s.invites[string(hashToken(inviteTokenFromURL(t, second)))].usedAt)
}

func TestIssueCLIInvite_RejectsNonPositiveTTL(t *testing.T) {
	_, _, err := IssueCLIInvite(context.Background(), newFakeStore(), "http://o", false, 0)
	assert.Error(t, err)
}
