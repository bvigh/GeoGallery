package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type fakeSession struct {
	userID    string
	idToken   string
	expiresAt time.Time
}

type fakeToken struct {
	APIToken
	userID string
	hash   string
}

type fakeInvite struct {
	Invite
	usedAt *time.Time
	usedBy string
}

type fakeUser struct {
	UserRecord
	issuer, subject string
}

// fakeStore is an in-memory Store. Set err to make every method fail.
type fakeStore struct {
	mu        sync.Mutex
	err       error
	touchErr  error
	users     map[string]*fakeUser
	sessions  map[string]*fakeSession
	tokens    map[string]*fakeToken // by id
	invites   map[string]*fakeInvite
	touched   []string
	issuerArg []string // issuer values passed to FindUserByIdentity/RegisterWithInvite
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users: map[string]*fakeUser{}, sessions: map[string]*fakeSession{},
		tokens: map[string]*fakeToken{}, invites: map[string]*fakeInvite{},
	}
}

func fakeID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:])
}

func (s *fakeStore) addUser(issuer, subject, email, role string, disabled bool) User {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := &fakeUser{UserRecord: UserRecord{
		User:    User{ID: fakeID(), Email: email, DisplayName: email, Role: role},
		Claimed: issuer != "", Disabled: disabled, CreatedAt: time.Now(),
	}, issuer: issuer, subject: subject}
	s.users[u.ID] = u
	return u.User
}

func (s *fakeStore) addSession(userID string, expiresAt time.Time) string {
	raw, hash := newToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[string(hash)] = &fakeSession{userID: userID, idToken: "idtoken-" + userID, expiresAt: expiresAt}
	return raw
}

func (s *fakeStore) addAPIToken(userID string, expiresAt time.Time, revoked bool) (raw string, id string) {
	raw, hash, hint := newAPIToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &fakeToken{APIToken: APIToken{ID: fakeID(), Name: "t", DisplayHint: hint, CreatedAt: time.Now(), ExpiresAt: expiresAt}, userID: userID, hash: string(hash)}
	if revoked {
		now := time.Now()
		t.RevokedAt = &now
	}
	s.tokens[t.ID] = t
	return raw, t.ID
}

func (s *fakeStore) addInvite(role, claimUserID string, expiresAt time.Time) string {
	raw, hash := newToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invites[string(hash)] = &fakeInvite{Invite: Invite{TokenHash: hash, Role: role, ClaimUserID: claimUserID, ExpiresAt: expiresAt}}
	return raw
}

func (s *fakeStore) usableUser(id string) (User, bool) {
	u, ok := s.users[id]
	if !ok || u.Disabled || !u.Claimed {
		return User{}, false
	}
	return u.User, true
}

func (s *fakeStore) UserForSession(_ context.Context, hash []byte) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return User{}, s.err
	}
	sess, ok := s.sessions[string(hash)]
	if !ok || !sess.expiresAt.After(time.Now()) {
		return User{}, ErrNoSession
	}
	u, ok := s.usableUser(sess.userID)
	if !ok {
		return User{}, ErrNoSession
	}
	return u, nil
}

func (s *fakeStore) UserForAPIToken(_ context.Context, hash []byte) (User, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return User{}, "", s.err
	}
	for _, t := range s.tokens {
		if t.hash != string(hash) {
			continue
		}
		if t.RevokedAt != nil || !t.ExpiresAt.After(time.Now()) {
			return User{}, "", ErrNoToken
		}
		u, ok := s.usableUser(t.userID)
		if !ok {
			return User{}, "", ErrNoToken
		}
		return u, t.ID, nil
	}
	return User{}, "", ErrNoToken
}

func (s *fakeStore) TouchAPIToken(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched = append(s.touched, id)
	return s.touchErr
}

func (s *fakeStore) activeTokens(userID string) int {
	n := 0
	for _, t := range s.tokens {
		if t.userID == userID && t.RevokedAt == nil && t.ExpiresAt.After(time.Now()) {
			n++
		}
	}
	return n
}

func (s *fakeStore) CreateAPIToken(_ context.Context, userID, name string, hash []byte, hint string, expiresAt time.Time, maxActive int) (APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return APIToken{}, s.err
	}
	if s.activeTokens(userID) >= maxActive {
		return APIToken{}, ErrTokenLimit
	}
	t := &fakeToken{APIToken: APIToken{ID: fakeID(), Name: name, DisplayHint: hint, CreatedAt: time.Now(), ExpiresAt: expiresAt}, userID: userID, hash: string(hash)}
	s.tokens[t.ID] = t
	return t.APIToken, nil
}

func (s *fakeStore) ListAPITokens(_ context.Context, userID string) ([]APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var out []APIToken
	for _, t := range s.tokens {
		if t.userID == userID {
			out = append(out, t.APIToken)
		}
	}
	return out, nil
}

func (s *fakeStore) RevokeAPIToken(_ context.Context, userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	t, ok := s.tokens[id]
	if !ok || t.userID != userID || t.RevokedAt != nil {
		return ErrNoToken
	}
	now := time.Now()
	t.RevokedAt = &now
	return nil
}

func (s *fakeStore) SessionIDToken(_ context.Context, hash []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	sess, ok := s.sessions[string(hash)]
	if !ok {
		return "", ErrNoSession
	}
	return sess.idToken, nil
}

func (s *fakeStore) CreateSession(_ context.Context, hash []byte, userID, idToken string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sessions[string(hash)] = &fakeSession{userID: userID, idToken: idToken, expiresAt: expiresAt}
	return nil
}

func (s *fakeStore) DeleteSession(_ context.Context, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.sessions, string(hash))
	return nil
}

func (s *fakeStore) FindUserByIdentity(_ context.Context, issuer, subject string) (UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issuerArg = append(s.issuerArg, issuer)
	if s.err != nil {
		return UserRecord{}, s.err
	}
	for _, u := range s.users {
		if u.issuer == issuer && u.subject == subject {
			return u.UserRecord, nil
		}
	}
	return UserRecord{}, ErrNoUser
}

func (s *fakeStore) TouchLogin(_ context.Context, userID, email, displayName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if u, ok := s.users[userID]; ok {
		if email != "" {
			u.Email = email
		}
		if displayName != "" {
			u.DisplayName = displayName
		}
		now := time.Now()
		u.LastLoginAt = &now
	}
	return nil
}

func (s *fakeStore) RegisterWithInvite(_ context.Context, inviteHash []byte, issuer, subject, email, displayName string) (UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issuerArg = append(s.issuerArg, issuer)
	if s.err != nil {
		return UserRecord{}, s.err
	}
	inv, ok := s.invites[string(inviteHash)]
	if !ok || inv.usedAt != nil || !inv.ExpiresAt.After(time.Now()) {
		return UserRecord{}, ErrInviteInvalid
	}
	var u *fakeUser
	if inv.ClaimUserID != "" {
		u = s.users[inv.ClaimUserID]
		if u == nil || u.Claimed || u.Disabled {
			return UserRecord{}, ErrInviteInvalid
		}
	} else {
		u = &fakeUser{UserRecord: UserRecord{User: User{ID: fakeID(), Role: inv.Role}, CreatedAt: time.Now()}}
		s.users[u.ID] = u
	}
	u.issuer, u.subject, u.Claimed, u.Email, u.DisplayName = issuer, subject, true, email, displayName
	now := time.Now()
	inv.usedAt, inv.usedBy = &now, u.ID
	return u.UserRecord, nil
}

func (s *fakeStore) CreateInvite(_ context.Context, inv Invite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if inv.ClaimUserID != "" {
		now := time.Now()
		for _, old := range s.invites {
			if old.ClaimUserID == inv.ClaimUserID && old.usedAt == nil {
				old.usedAt = &now
			}
		}
	}
	s.invites[string(inv.TokenHash)] = &fakeInvite{Invite: inv}
	return nil
}

func (s *fakeStore) ListUsers(context.Context) ([]UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := []UserRecord{}
	for _, u := range s.users {
		out = append(out, u.UserRecord)
	}
	return out, nil
}

func (s *fakeStore) SetDisabled(_ context.Context, userID string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	u, ok := s.users[userID]
	if !ok {
		return ErrNoUser
	}
	u.Disabled = disabled
	return nil
}

func (s *fakeStore) RebindIssuer(_ context.Context, from, to string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, u := range s.users {
		if u.issuer == from {
			u.issuer = to
			n++
		}
	}
	return n, s.err
}

func (s *fakeStore) DeleteExpiredSessions(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, sess := range s.sessions {
		if !sess.expiresAt.After(time.Now()) {
			delete(s.sessions, k)
		}
	}
	return nil
}

func (s *fakeStore) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *fakeStore) hasSession(raw string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[string(hashToken(raw))]
	return ok
}

var _ Store = (*fakeStore)(nil)
