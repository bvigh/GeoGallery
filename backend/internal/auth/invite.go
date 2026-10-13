package auth

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

const LegacyOwnerID = "00000000-0000-0000-0000-000000000001"

func InviteURL(publicOrigin, rawToken string) string {
	return publicOrigin + "/api/auth/login?invite=" + url.QueryEscape(rawToken)
}

// CreateInvite stores a plain (non-claim) invite and returns its login URL.
func CreateInvite(ctx context.Context, store Store, publicOrigin, role, createdBy string, ttl time.Duration) (string, error) {
	raw, hash := newToken()
	if err := store.CreateInvite(ctx, Invite{TokenHash: hash, Role: role, CreatedBy: createdBy, ExpiresAt: time.Now().Add(ttl)}); err != nil {
		return "", err
	}
	return InviteURL(publicOrigin, raw), nil
}

// IssueCLIInvite implements `server invite`. With admin set and an unclaimed admin present
// (the legacy Owner first), the invite claims that user; the store revokes any earlier
// open claim invite for it, so re-running after a lost link is safe.
func IssueCLIInvite(ctx context.Context, store Store, publicOrigin string, admin bool, ttl time.Duration) (inviteURL, claimUserID string, err error) {
	if ttl <= 0 {
		return "", "", fmt.Errorf("ttl must be positive")
	}
	role := "user"
	if admin {
		role = "admin"
		users, err := store.ListUsers(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list users: %w", err)
		}
		claimUserID = pickUnclaimedAdmin(users)
	}
	if claimUserID == "" {
		url, err := CreateInvite(ctx, store, publicOrigin, role, "", ttl)
		return url, "", err
	}
	raw, hash := newToken()
	if err := store.CreateInvite(ctx, Invite{TokenHash: hash, Role: role, ClaimUserID: claimUserID, ExpiresAt: time.Now().Add(ttl)}); err != nil {
		return "", "", err
	}
	return InviteURL(publicOrigin, raw), claimUserID, nil
}

func pickUnclaimedAdmin(users []UserRecord) string {
	first := ""
	for _, u := range users {
		if u.Claimed || u.Disabled || !u.IsAdmin() {
			continue
		}
		if u.ID == LegacyOwnerID {
			return u.ID
		}
		if first == "" {
			first = u.ID
		}
	}
	return first
}
