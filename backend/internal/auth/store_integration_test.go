package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth/oidctest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIssuer = "https://idp.example.test"

func newPGStore(t *testing.T) (*PGStore, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.New(t)
	return NewPGStore(pool), pool
}

func insertUser(t *testing.T, pool *pgxpool.Pool, issuer, subject, role string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email, role) VALUES (NULLIF($1, ''), NULLIF($2, ''), $2 || '@example.test', $3)
		RETURNING id::text`, issuer, subject, role).Scan(&id)
	require.NoError(t, err)
	return id
}

func createInvite(t *testing.T, s *PGStore, role, claim string, expiresAt time.Time) string {
	t.Helper()
	raw, hash := newToken()
	require.NoError(t, s.CreateInvite(context.Background(), Invite{TokenHash: hash, Role: role, ClaimUserID: claim, ExpiresAt: expiresAt}))
	return raw
}

// insertInviteRaw bypasses CreateInvite, so an earlier claim invite for the same user stays open.
func insertInviteRaw(t *testing.T, pool *pgxpool.Pool, role, claim string) string {
	t.Helper()
	raw, hash := newToken()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO invites (token_hash, role, claim_user_id, expires_at) VALUES ($1, $2, NULLIF($3, '')::uuid, NOW() + INTERVAL '1 hour')`,
		hash, role, claim)
	require.NoError(t, err)
	return raw
}

func countUsers(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&n))
	return n
}

func TestPGStore_RegisterWithInvite(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()

	t.Run("plain invite creates user once; reuse is invalid", func(t *testing.T) {
		raw := createInvite(t, s, "user", "", time.Now().Add(time.Hour))
		u, err := s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "new-1", "n1@example.test", "New One")
		require.NoError(t, err)
		assert.Equal(t, "user", u.Role)
		assert.True(t, u.Claimed)
		assert.Equal(t, "n1@example.test", u.Email)

		var usedBy string
		require.NoError(t, pool.QueryRow(ctx, `SELECT used_by::text FROM invites WHERE token_hash = $1 AND used_at IS NOT NULL`, hashToken(raw)).Scan(&usedBy))
		assert.Equal(t, u.ID, usedBy)

		_, err = s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "new-2", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)

		found, err := s.FindUserByIdentity(ctx, testIssuer, "new-1")
		require.NoError(t, err)
		assert.Equal(t, u.ID, found.ID)
		_, err = s.FindUserByIdentity(ctx, testIssuer, "new-2")
		assert.ErrorIs(t, err, ErrNoUser)
	})

	t.Run("expired invite is invalid", func(t *testing.T) {
		raw := createInvite(t, s, "user", "", time.Now().Add(-time.Minute))
		_, err := s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "late", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)
	})

	t.Run("unknown invite is invalid", func(t *testing.T) {
		_, err := s.RegisterWithInvite(ctx, hashToken("nope"), testIssuer, "x", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)
	})

	t.Run("legacy owner claim binds the existing row; second claim invite is invalid", func(t *testing.T) {
		before := countUsers(t, pool)
		first := createInvite(t, s, "admin", LegacyOwnerID, time.Now().Add(time.Hour))
		second := insertInviteRaw(t, pool, "admin", LegacyOwnerID)

		u, err := s.RegisterWithInvite(ctx, hashToken(first), testIssuer, "owner-sub", "owner@example.test", "Owner Person")
		require.NoError(t, err)
		assert.Equal(t, LegacyOwnerID, u.ID)
		assert.Equal(t, "admin", u.Role)
		assert.Equal(t, before, countUsers(t, pool), "no new row")

		_, err = s.RegisterWithInvite(ctx, hashToken(second), "https://attacker.example", "attacker", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)

		var iss, sub string
		require.NoError(t, pool.QueryRow(ctx, `SELECT issuer, subject FROM users WHERE id = $1`, LegacyOwnerID).Scan(&iss, &sub))
		assert.Equal(t, testIssuer, iss)
		assert.Equal(t, "owner-sub", sub)

		var usedAt *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT used_at FROM invites WHERE token_hash = $1`, hashToken(second)).Scan(&usedAt))
		assert.Nil(t, usedAt, "failed claim rolls back the invite consumption")
	})

	t.Run("claim invite for a disabled unclaimed user is invalid and not consumed", func(t *testing.T) {
		id := insertUser(t, pool, "", "", "user")
		raw := createInvite(t, s, "user", id, time.Now().Add(time.Hour))
		require.NoError(t, s.SetDisabled(ctx, id, true))

		_, err := s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "disabled-claimer", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)
		var iss *string
		var usedAt *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT issuer FROM users WHERE id = $1`, id).Scan(&iss))
		assert.Nil(t, iss, "identity not bound")
		require.NoError(t, pool.QueryRow(ctx, `SELECT used_at FROM invites WHERE token_hash = $1`, hashToken(raw)).Scan(&usedAt))
		assert.Nil(t, usedAt, "invite not consumed")

		require.NoError(t, s.SetDisabled(ctx, id, false))
		u, err := s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "disabled-claimer", "", "")
		require.NoError(t, err)
		assert.Equal(t, id, u.ID)
	})
}

func TestPGStore_ConcurrentClaimInvites(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	for i := range 5 {
		target := insertUser(t, pool, "", "", "admin")
		a := insertInviteRaw(t, pool, "admin", target)
		b := insertInviteRaw(t, pool, "admin", target)

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for j, raw := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[j] = s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "racer-"+string(rune('a'+i))+string(rune('0'+j)), "", "")
			}()
		}
		close(start)
		wg.Wait()

		ok, invalid := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrInviteInvalid):
				invalid++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		assert.Equal(t, 1, ok, "iteration %d", i)
		assert.Equal(t, 1, invalid, "iteration %d", i)
	}
}

func TestPGStore_CLIInviteRevokesEarlierClaimInvite(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()

	firstURL, claim, err := IssueCLIInvite(ctx, s, "http://localhost:5173", true, 72*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, LegacyOwnerID, claim)
	secondURL, _, err := IssueCLIInvite(ctx, s, "http://localhost:5173", true, 72*time.Hour)
	require.NoError(t, err)

	first := inviteTokenFromURL(t, firstURL)
	second := inviteTokenFromURL(t, secondURL)
	_, err = s.RegisterWithInvite(ctx, hashToken(first), testIssuer, "lost-link", "", "")
	assert.ErrorIs(t, err, ErrInviteInvalid, "earlier claim invite was revoked")
	u, err := s.RegisterWithInvite(ctx, hashToken(second), testIssuer, "owner", "", "")
	require.NoError(t, err)
	assert.Equal(t, LegacyOwnerID, u.ID)

	// Once the owner is claimed, --admin falls back to a plain admin invite.
	thirdURL, claim, err := IssueCLIInvite(ctx, s, "http://localhost:5173", true, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, claim)
	var role string
	var claimID *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT role, claim_user_id::text FROM invites WHERE token_hash = $1`,
		hashToken(inviteTokenFromURL(t, thirdURL))).Scan(&role, &claimID))
	assert.Equal(t, "admin", role)
	assert.Nil(t, claimID)
}

func TestPGStore_Sessions(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	alice := insertUser(t, pool, testIssuer, "alice", "user")

	live, liveHash := newToken()
	_, expiredHash := newToken()
	require.NoError(t, s.CreateSession(ctx, liveHash, alice, "id-token-live", time.Now().Add(time.Hour)))
	require.NoError(t, s.CreateSession(ctx, expiredHash, alice, "id-token-old", time.Now().Add(-time.Hour)))

	u, err := s.UserForSession(ctx, hashToken(live))
	require.NoError(t, err)
	assert.Equal(t, alice, u.ID)
	assert.Equal(t, "alice@example.test", u.Email)
	_, err = s.UserForSession(ctx, expiredHash)
	assert.ErrorIs(t, err, ErrNoSession)
	_, err = s.UserForSession(ctx, hashToken("unknown"))
	assert.ErrorIs(t, err, ErrNoSession)

	idt, err := s.SessionIDToken(ctx, liveHash)
	require.NoError(t, err)
	assert.Equal(t, "id-token-live", idt)
	_, err = s.SessionIDToken(ctx, hashToken("unknown"))
	assert.ErrorIs(t, err, ErrNoSession)

	require.NoError(t, s.DeleteExpiredSessions(ctx))
	_, err = s.SessionIDToken(ctx, expiredHash)
	assert.ErrorIs(t, err, ErrNoSession)

	require.NoError(t, s.DeleteSession(ctx, liveHash))
	_, err = s.UserForSession(ctx, liveHash)
	assert.ErrorIs(t, err, ErrNoSession)

	// Unclaimed users cannot hold a working session, even one inserted behind the store's back.
	_, ownerHash := newToken()
	_, err = pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, id_token, expires_at) VALUES ($1, $2, 'x', NOW() + INTERVAL '1 hour')`,
		ownerHash, LegacyOwnerID)
	require.NoError(t, err)
	_, err = s.UserForSession(ctx, ownerHash)
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestPGStore_CreateForInactiveUser(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	disabled := insertUser(t, pool, testIssuer, "dis", "user")
	require.NoError(t, s.SetDisabled(ctx, disabled, true))

	tests := []struct {
		name   string
		userID string
	}{
		{"disabled", disabled},
		{"unclaimed", LegacyOwnerID},
		{"unknown", "00000000-0000-0000-0000-00000000dead"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, sessHash := newToken()
			assert.ErrorIs(t, s.CreateSession(ctx, sessHash, tt.userID, "x", time.Now().Add(time.Hour)), ErrNoUser)
			_, tokHash, hint := newAPIToken()
			_, err := s.CreateAPIToken(ctx, tt.userID, "t", tokHash, hint, time.Now().Add(time.Hour), 25)
			assert.ErrorIs(t, err, ErrNoUser)

			var sessions, tokens int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id::text = $1`, tt.userID).Scan(&sessions))
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id::text = $1`, tt.userID).Scan(&tokens))
			assert.Zero(t, sessions)
			assert.Zero(t, tokens)
		})
	}
}

func TestPGStore_SetDisabledWaitsForInFlightSession(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	u := insertUser(t, pool, testIssuer, "racer", "user")

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	require.NoError(t, lockActiveUser(ctx, tx, u))
	_, sessHash := newToken()
	_, err = tx.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, id_token, expires_at) VALUES ($1, $2, 'x', NOW() + INTERVAL '1 hour')`,
		sessHash, u)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- s.SetDisabled(ctx, u, true) }()
	select {
	case err := <-done:
		t.Fatalf("SetDisabled finished while the session insert was uncommitted (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, tx.Commit(ctx))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("SetDisabled did not finish after the insert committed")
	}
	_, err = s.UserForSession(ctx, sessHash)
	assert.ErrorIs(t, err, ErrNoSession, "session inserted before the disable is deleted by it")
}

func TestPGStore_InvitesFromDisabledCreator(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	usedAt := func(raw string) *time.Time {
		var ts *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT used_at FROM invites WHERE token_hash = $1`, hashToken(raw)).Scan(&ts))
		return ts
	}
	inviteBy := func(creator string) string {
		raw, hash := newToken()
		require.NoError(t, s.CreateInvite(ctx, Invite{TokenHash: hash, Role: "user", CreatedBy: creator, ExpiresAt: time.Now().Add(time.Hour)}))
		return raw
	}

	t.Run("SetDisabled revokes the creator's open invites", func(t *testing.T) {
		b := insertUser(t, pool, testIssuer, "creator-b", "admin")
		other := insertUser(t, pool, testIssuer, "creator-other", "admin")
		raw := inviteBy(b)
		otherRaw := inviteBy(other)

		require.NoError(t, s.SetDisabled(ctx, b, true))
		assert.NotNil(t, usedAt(raw), "invite revoked")
		assert.Nil(t, usedAt(otherRaw), "other creator's invite untouched")

		_, err := s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "via-disabled-b", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)
		_, err = s.FindUserByIdentity(ctx, testIssuer, "via-disabled-b")
		assert.ErrorIs(t, err, ErrNoUser)

		require.NoError(t, s.SetDisabled(ctx, b, false))
		_, err = s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "via-reenabled-b", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid, "re-enabling never revives invites")
	})

	t.Run("consume rejects invites whose creator is disabled", func(t *testing.T) {
		b := insertUser(t, pool, testIssuer, "creator-c", "admin")
		raw := inviteBy(b)
		_, err := pool.Exec(ctx, `UPDATE users SET disabled_at = NOW() WHERE id = $1`, b)
		require.NoError(t, err)

		_, err = s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "via-disabled-c", "", "")
		assert.ErrorIs(t, err, ErrInviteInvalid)
		assert.Nil(t, usedAt(raw), "not consumed")
		_, err = s.FindUserByIdentity(ctx, testIssuer, "via-disabled-c")
		assert.ErrorIs(t, err, ErrNoUser)
	})

	t.Run("invite from an enabled creator is accepted", func(t *testing.T) {
		b := insertUser(t, pool, testIssuer, "creator-d", "admin")
		raw := inviteBy(b)
		u, err := s.RegisterWithInvite(ctx, hashToken(raw), testIssuer, "via-enabled-d", "", "")
		require.NoError(t, err)
		assert.Equal(t, "user", u.Role)
	})
}

func TestPGStore_TouchLogin(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	id := insertUser(t, pool, testIssuer, "alice", "user")

	require.NoError(t, s.TouchLogin(ctx, id, "new@example.test", "Alice New"))
	u, err := s.FindUserByIdentity(ctx, testIssuer, "alice")
	require.NoError(t, err)
	assert.Equal(t, "new@example.test", u.Email)
	assert.Equal(t, "Alice New", u.DisplayName)
	require.NotNil(t, u.LastLoginAt)

	require.NoError(t, s.TouchLogin(ctx, id, "", ""))
	u, err = s.FindUserByIdentity(ctx, testIssuer, "alice")
	require.NoError(t, err)
	assert.Equal(t, "new@example.test", u.Email, "empty claims do not erase stored values")
	assert.Equal(t, "Alice New", u.DisplayName)
}

func TestPGStore_SetDisabled(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	alice := insertUser(t, pool, testIssuer, "alice", "user")
	bob := insertUser(t, pool, testIssuer, "bob", "user")
	a := newTestAuth(s, nil)

	_, aliceSession := newToken()
	_, bobSession := newToken()
	require.NoError(t, s.CreateSession(ctx, aliceSession, alice, "x", time.Now().Add(time.Hour)))
	require.NoError(t, s.CreateSession(ctx, bobSession, bob, "x", time.Now().Add(time.Hour)))
	aliceToken, aliceHash, hint := newAPIToken()
	tok, err := s.CreateAPIToken(ctx, alice, "cron", aliceHash, hint, time.Now().Add(time.Hour), 25)
	require.NoError(t, err)
	_, bobHash, bobHint := newAPIToken()
	_, err = s.CreateAPIToken(ctx, bob, "cron", bobHash, bobHint, time.Now().Add(time.Hour), 25)
	require.NoError(t, err)

	tokenStatus := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rec := httptest.NewRecorder()
		a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, tokenStatus())

	require.NoError(t, s.SetDisabled(ctx, alice, true))
	_, err = s.UserForSession(ctx, aliceSession)
	assert.ErrorIs(t, err, ErrNoSession)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, alice).Scan(&n))
	assert.Zero(t, n, "sessions deleted")
	assert.Equal(t, http.StatusUnauthorized, tokenStatus())

	users, err := s.ListUsers(ctx)
	require.NoError(t, err)
	for _, u := range users {
		if u.ID == alice {
			assert.True(t, u.Disabled)
		}
	}

	require.NoError(t, s.SetDisabled(ctx, alice, false))
	assert.Equal(t, http.StatusUnauthorized, tokenStatus(), "re-enabling never revives tokens")
	var revokedAt *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&revokedAt))
	assert.NotNil(t, revokedAt)

	// Bob is untouched.
	_, err = s.UserForSession(ctx, bobSession)
	assert.NoError(t, err)
	_, _, err = s.UserForAPIToken(ctx, bobHash)
	assert.NoError(t, err)

	assert.ErrorIs(t, s.SetDisabled(ctx, "00000000-0000-0000-0000-00000000dead", true), ErrNoUser)
}

func TestPGStore_RebindIssuer(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	insertUser(t, pool, "https://old.example", "a", "user")
	insertUser(t, pool, "https://old.example", "b", "user")
	other := insertUser(t, pool, "https://other.example", "a", "user")

	n, err := s.RebindIssuer(ctx, "https://old.example", "https://new.example")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	_, err = s.FindUserByIdentity(ctx, "https://old.example", "a")
	assert.ErrorIs(t, err, ErrNoUser)
	_, err = s.FindUserByIdentity(ctx, "https://new.example", "b")
	assert.NoError(t, err)
	u, err := s.FindUserByIdentity(ctx, "https://other.example", "a")
	require.NoError(t, err)
	assert.Equal(t, other, u.ID)

	n, err = s.RebindIssuer(ctx, "https://nobody.example", "https://x.example")
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestPGStore_ListUsers(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	active := insertUser(t, pool, testIssuer, "a", "user")
	disabled := insertUser(t, pool, testIssuer, "d", "user")
	require.NoError(t, s.SetDisabled(ctx, disabled, true))

	users, err := s.ListUsers(ctx)
	require.NoError(t, err)
	byID := map[string]UserRecord{}
	for _, u := range users {
		byID[u.ID] = u
	}
	require.Len(t, byID, 3)
	assert.False(t, byID[LegacyOwnerID].Claimed)
	assert.Equal(t, "admin", byID[LegacyOwnerID].Role)
	assert.Equal(t, "Owner", byID[LegacyOwnerID].DisplayName)
	assert.Empty(t, byID[LegacyOwnerID].Email)
	assert.True(t, byID[active].Claimed)
	assert.False(t, byID[active].Disabled)
	assert.True(t, byID[disabled].Disabled)
	assert.Equal(t, LegacyOwnerID, users[0].ID, "ordered by created_at")
}

func TestPGStore_APITokens(t *testing.T) {
	s, pool := newPGStore(t)
	ctx := context.Background()
	alice := insertUser(t, pool, testIssuer, "alice", "user")
	bob := insertUser(t, pool, testIssuer, "bob", "user")
	disabled := insertUser(t, pool, testIssuer, "dis", "user")

	mk := func(userID string, expiresAt time.Time) (string, []byte, APIToken) {
		raw, hash, hint := newAPIToken()
		tok, err := s.CreateAPIToken(ctx, userID, "t", hash, hint, expiresAt, 25)
		require.NoError(t, err)
		return raw, hash, tok
	}

	t.Run("UserForAPIToken", func(t *testing.T) {
		_, okHash, okTok := mk(alice, time.Now().Add(time.Hour))
		_, revokedHash, revokedTok := mk(alice, time.Now().Add(time.Hour))
		require.NoError(t, s.RevokeAPIToken(ctx, alice, revokedTok.ID))
		_, expiredHash, _ := mk(alice, time.Now().Add(-time.Minute))
		_, disabledHash, _ := mk(disabled, time.Now().Add(time.Hour))
		require.NoError(t, s.SetDisabled(ctx, disabled, true))
		_, unclaimedHash, _ := newAPIToken()
		_, err := pool.Exec(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, display_hint, expires_at)
			VALUES ($1, 't', $2, 'gg_pat_x', NOW() + INTERVAL '1 hour')`, LegacyOwnerID, unclaimedHash)
		require.NoError(t, err)

		u, id, err := s.UserForAPIToken(ctx, okHash)
		require.NoError(t, err)
		assert.Equal(t, alice, u.ID)
		assert.Equal(t, okTok.ID, id)

		for name, h := range map[string][]byte{"revoked": revokedHash, "expired": expiredHash, "disabled user": disabledHash,
			"unclaimed user": unclaimedHash, "unknown": hashToken("gg_pat_unknown")} {
			_, _, err := s.UserForAPIToken(ctx, h)
			assert.ErrorIs(t, err, ErrNoToken, name)
		}
	})

	t.Run("TouchAPIToken is throttled", func(t *testing.T) {
		_, _, tok := mk(alice, time.Now().Add(time.Hour))
		lastUsed := func() *time.Time {
			var ts *time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&ts))
			return ts
		}
		require.Nil(t, lastUsed())
		require.NoError(t, s.TouchAPIToken(ctx, tok.ID))
		first := lastUsed()
		require.NotNil(t, first)

		time.Sleep(10 * time.Millisecond)
		require.NoError(t, s.TouchAPIToken(ctx, tok.ID))
		assert.Equal(t, first.UnixMicro(), lastUsed().UnixMicro(), "second touch within 5 minutes is a no-op")

		_, err := pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = NOW() - INTERVAL '6 minutes' WHERE id = $1`, tok.ID)
		require.NoError(t, err)
		require.NoError(t, s.TouchAPIToken(ctx, tok.ID))
		assert.WithinDuration(t, time.Now(), *lastUsed(), 30*time.Second)
	})

	t.Run("List and Revoke are scoped to the user", func(t *testing.T) {
		_, _, bobTok := mk(bob, time.Now().Add(time.Hour))

		aliceList, err := s.ListAPITokens(ctx, alice)
		require.NoError(t, err)
		for _, tok := range aliceList {
			assert.NotEqual(t, bobTok.ID, tok.ID)
		}
		bobList, err := s.ListAPITokens(ctx, bob)
		require.NoError(t, err)
		require.Len(t, bobList, 1)
		assert.Equal(t, bobTok.ID, bobList[0].ID)
		assert.Equal(t, "t", bobList[0].Name)

		assert.ErrorIs(t, s.RevokeAPIToken(ctx, alice, bobTok.ID), ErrNoToken)
		require.NoError(t, s.RevokeAPIToken(ctx, bob, bobTok.ID))
		assert.ErrorIs(t, s.RevokeAPIToken(ctx, bob, bobTok.ID), ErrNoToken, "already revoked")

		bobList, err = s.ListAPITokens(ctx, bob)
		require.NoError(t, err)
		require.Len(t, bobList, 1)
		assert.NotNil(t, bobList[0].RevokedAt, "revoked tokens stay in the history")

		empty, err := s.ListAPITokens(ctx, LegacyOwnerID)
		require.NoError(t, err)
		assert.NotNil(t, empty)
	})

	t.Run("active-token cap counts only unrevoked unexpired tokens", func(t *testing.T) {
		carol := insertUser(t, pool, testIssuer, "carol", "user")
		_, _, revoked := mk(carol, time.Now().Add(time.Hour))
		require.NoError(t, s.RevokeAPIToken(ctx, carol, revoked.ID))
		mk(carol, time.Now().Add(-time.Hour))
		for range 2 {
			mk(carol, time.Now().Add(time.Hour))
		}
		_, hash, hint := newAPIToken()
		_, err := s.CreateAPIToken(ctx, carol, "third", hash, hint, time.Now().Add(time.Hour), 3)
		require.NoError(t, err)
		_, hash, hint = newAPIToken()
		_, err = s.CreateAPIToken(ctx, carol, "fourth", hash, hint, time.Now().Add(time.Hour), 3)
		assert.ErrorIs(t, err, ErrTokenLimit)
	})
}

func TestPGStore_CallbackEndToEnd(t *testing.T) {
	s, _ := newPGStore(t)
	ctx := context.Background()
	iss := oidctest.New(t)
	cfg := providerConfig(iss)
	p, err := NewProvider(ctx, cfg)
	require.NoError(t, err)
	e := &loginEnv{iss: iss, store: s, cfg: cfg, a: New(s, p, cfg)}

	inviteURL, claim, err := IssueCLIInvite(ctx, s, cfg.PublicOrigin, true, time.Hour)
	require.NoError(t, err)
	require.Equal(t, LegacyOwnerID, claim)

	rec := e.login(t, "invite="+inviteTokenFromURL(t, inviteURL)+"&return_to=%2Ftrips")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/trips", rec.Header().Get("Location"))
	raw := sessionValue(rec)
	require.NotEmpty(t, raw)

	u, err := s.UserForSession(ctx, hashToken(raw))
	require.NoError(t, err)
	assert.Equal(t, LegacyOwnerID, u.ID)
	assert.Equal(t, "alice@example.test", u.Email)
	assert.True(t, u.IsAdmin())

	// A second login (no invite) finds the claimed owner and rotates the session.
	rec = e.login(t, "", &http.Cookie{Name: "gg_session", Value: raw})
	require.Equal(t, http.StatusFound, rec.Code)
	raw2 := sessionValue(rec)
	require.NotEmpty(t, raw2)
	_, err = s.UserForSession(ctx, hashToken(raw))
	assert.ErrorIs(t, err, ErrNoSession)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "gg_session", Value: raw2})
	logoutRec := httptest.NewRecorder()
	e.a.Logout(logoutRec, req)
	require.Equal(t, http.StatusOK, logoutRec.Code)
	assert.Contains(t, logoutRec.Body.String(), "id_token_hint")
	_, err = s.UserForSession(ctx, hashToken(raw2))
	assert.ErrorIs(t, err, ErrNoSession)

	// An uninvited identity is rejected.
	iss.Set(func(i *oidctest.Issuer) { i.Sub = "stranger" })
	rec = e.login(t, "")
	assert.Equal(t, "/?auth_error=not_invited", rec.Header().Get("Location"))
}
