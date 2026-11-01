package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoUser        = errors.New("auth: user not found")
	ErrNoSession     = errors.New("auth: session not found")
	ErrNoToken       = errors.New("auth: api token not found")
	ErrInviteInvalid = errors.New("auth: invite invalid, used or expired")
	ErrTokenLimit    = errors.New("auth: active api token limit reached")
)

type APIToken struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	DisplayHint string     `json:"display_hint"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

type Invite struct {
	TokenHash   []byte
	Role        string
	ClaimUserID string // "" for a plain invite; CLI-only
	CreatedBy   string // "" when created from the CLI
	ExpiresAt   time.Time
}

type Store interface {
	UserForSession(ctx context.Context, tokenHash []byte) (User, error)
	UserForAPIToken(ctx context.Context, tokenHash []byte) (u User, tokenID string, err error)
	TouchAPIToken(ctx context.Context, tokenID string) error
	// CreateAPIToken inserts a token unless the user already has maxActive unrevoked, unexpired
	// tokens, in which case it returns ErrTokenLimit. ErrNoUser if the user is disabled or unclaimed.
	CreateAPIToken(ctx context.Context, userID, name string, tokenHash []byte, displayHint string, expiresAt time.Time, maxActive int) (APIToken, error)
	ListAPITokens(ctx context.Context, userID string) ([]APIToken, error)
	RevokeAPIToken(ctx context.Context, userID, tokenID string) error
	SessionIDToken(ctx context.Context, tokenHash []byte) (string, error)
	// CreateSession returns ErrNoUser if the user is disabled or unclaimed.
	CreateSession(ctx context.Context, tokenHash []byte, userID, idToken string, expiresAt time.Time) error
	DeleteSession(ctx context.Context, tokenHash []byte) error
	FindUserByIdentity(ctx context.Context, issuer, subject string) (UserRecord, error)
	TouchLogin(ctx context.Context, userID, email, displayName string) error
	RegisterWithInvite(ctx context.Context, inviteHash []byte, issuer, subject, email, displayName string) (UserRecord, error)
	// CreateInvite revokes any earlier unused invite for the same ClaimUserID in the same transaction.
	CreateInvite(ctx context.Context, inv Invite) error
	ListUsers(ctx context.Context) ([]UserRecord, error)
	// SetDisabled(true) also deletes the user's sessions and revokes all their API tokens and
	// unused invites they created.
	SetDisabled(ctx context.Context, userID string, disabled bool) error
	RebindIssuer(ctx context.Context, from, to string) (int64, error)
	DeleteExpiredSessions(ctx context.Context) error
}

type PGStore struct {
	db *pgxpool.Pool
}

func NewPGStore(db *pgxpool.Pool) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

const userRecordCols = `id::text, COALESCE(email, ''), COALESCE(display_name, ''), role,
	issuer IS NOT NULL, disabled_at IS NOT NULL, created_at, last_login_at`

func scanUserRecord(row pgx.Row) (UserRecord, error) {
	var u UserRecord
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Claimed, &u.Disabled, &u.CreatedAt, &u.LastLoginAt)
	return u, err
}

func (s *PGStore) UserForSession(ctx context.Context, tokenHash []byte) (User, error) {
	var u User
	err := s.db.QueryRow(ctx, `
		SELECT u.id::text, COALESCE(u.email, ''), COALESCE(u.display_name, ''), u.role
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > NOW()
		  AND u.disabled_at IS NULL AND u.issuer IS NOT NULL`, tokenHash,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoSession
	}
	return u, err
}

func (s *PGStore) UserForAPIToken(ctx context.Context, tokenHash []byte) (User, string, error) {
	var u User
	var tokenID string
	err := s.db.QueryRow(ctx, `
		SELECT u.id::text, COALESCE(u.email, ''), COALESCE(u.display_name, ''), u.role, t.id::text
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND t.revoked_at IS NULL AND t.expires_at > NOW()
		  AND u.disabled_at IS NULL AND u.issuer IS NOT NULL`, tokenHash,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &tokenID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrNoToken
	}
	return u, tokenID, err
}

// TouchAPIToken is throttled to one write per token per 5 minutes so a busy script does
// not cause a write per request.
func (s *PGStore) TouchAPIToken(ctx context.Context, tokenID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE api_tokens SET last_used_at = NOW()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < NOW() - INTERVAL '5 minutes')`, tokenID)
	return err
}

const apiTokenCols = `id::text, name, display_hint, created_at, expires_at, last_used_at, revoked_at`

func scanAPIToken(row pgx.Row) (APIToken, error) {
	var t APIToken
	err := row.Scan(&t.ID, &t.Name, &t.DisplayHint, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	return t, err
}

func (s *PGStore) CreateAPIToken(ctx context.Context, userID, name string, tokenHash []byte, displayHint string, expiresAt time.Time, maxActive int) (APIToken, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return APIToken{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockActiveUser(ctx, tx, userID); err != nil {
		return APIToken{}, err
	}
	t, err := scanAPIToken(tx.QueryRow(ctx, `
		INSERT INTO api_tokens (user_id, name, token_hash, display_hint, expires_at)
		SELECT $1::uuid, $2::text, $3::bytea, $4::text, $5::timestamptz
		WHERE (SELECT COUNT(*) FROM api_tokens
		       WHERE user_id = $1::uuid AND revoked_at IS NULL AND expires_at > NOW()) < $6::bigint
		RETURNING `+apiTokenCols,
		userID, name, tokenHash, displayHint, expiresAt, maxActive))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIToken{}, ErrTokenLimit
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("insert api token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return APIToken{}, err
	}
	return t, nil
}

// lockActiveUser takes a FOR SHARE lock on an enabled, claimed user so a concurrent SetDisabled
// (which locks the row for UPDATE) either waits for the insert to commit, and then revokes it, or
// runs first and makes this return ErrNoUser.
func lockActiveUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM users WHERE id = $1 AND disabled_at IS NULL AND issuer IS NOT NULL FOR SHARE`, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoUser
	}
	if err != nil {
		return fmt.Errorf("lock user: %w", err)
	}
	return nil
}

func (s *PGStore) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.db.Query(ctx, `SELECT `+apiTokenCols+` FROM api_tokens
		WHERE user_id = $1 ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *PGStore) RevokeAPIToken(ctx context.Context, userID, tokenID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE api_tokens SET revoked_at = NOW()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, tokenID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoToken
	}
	return nil
}

func (s *PGStore) SessionIDToken(ctx context.Context, tokenHash []byte) (string, error) {
	var idToken string
	err := s.db.QueryRow(ctx, `SELECT id_token FROM sessions WHERE token_hash = $1`, tokenHash).Scan(&idToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSession
	}
	return idToken, err
}

func (s *PGStore) CreateSession(ctx context.Context, tokenHash []byte, userID, idToken string, expiresAt time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockActiveUser(ctx, tx, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, id_token, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, userID, idToken, expiresAt); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PGStore) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *PGStore) FindUserByIdentity(ctx context.Context, issuer, subject string) (UserRecord, error) {
	u, err := scanUserRecord(s.db.QueryRow(ctx,
		`SELECT `+userRecordCols+` FROM users WHERE issuer = $1 AND subject = $2`, issuer, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, ErrNoUser
	}
	return u, err
}

func (s *PGStore) TouchLogin(ctx context.Context, userID, email, displayName string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE users SET email = COALESCE(NULLIF($2::text, ''), email),
		                 display_name = COALESCE(NULLIF($3::text, ''), display_name),
		                 last_login_at = NOW()
		WHERE id = $1`, userID, email, displayName)
	return err
}

func (s *PGStore) RegisterWithInvite(ctx context.Context, inviteHash []byte, issuer, subject, email, displayName string) (UserRecord, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return UserRecord{}, err
	}
	defer tx.Rollback(ctx)

	var role string
	var claimUserID *string
	err = tx.QueryRow(ctx, `
		UPDATE invites SET used_at = NOW()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()
		  AND (created_by IS NULL OR EXISTS (
		        SELECT 1 FROM users c WHERE c.id = invites.created_by AND c.disabled_at IS NULL))
		RETURNING role, claim_user_id::text`, inviteHash).Scan(&role, &claimUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, ErrInviteInvalid
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("consume invite: %w", err)
	}

	var u UserRecord
	if claimUserID != nil {
		// Guarded by issuer IS NULL: under READ COMMITTED a concurrent claim waits on the row
		// lock, re-checks the predicate and affects 0 rows, so a claimed user is never overwritten.
		// disabled_at IS NULL rolls back instead of burning the invite on an account that cannot log in.
		u, err = scanUserRecord(tx.QueryRow(ctx, `
			UPDATE users SET issuer = $2, subject = $3, email = NULLIF($4::text, ''), display_name = NULLIF($5::text, '')
			WHERE id = $1 AND issuer IS NULL AND disabled_at IS NULL
			RETURNING `+userRecordCols, *claimUserID, issuer, subject, email, displayName))
		if errors.Is(err, pgx.ErrNoRows) {
			return UserRecord{}, ErrInviteInvalid
		}
	} else {
		u, err = scanUserRecord(tx.QueryRow(ctx, `
			INSERT INTO users (issuer, subject, email, display_name, role)
			VALUES ($1, $2, NULLIF($3::text, ''), NULLIF($4::text, ''), $5)
			RETURNING `+userRecordCols, issuer, subject, email, displayName, role))
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("bind user: %w", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE invites SET used_by = $2 WHERE token_hash = $1`, inviteHash, u.ID); err != nil {
		return UserRecord{}, fmt.Errorf("mark invite used: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return UserRecord{}, err
	}
	return u, nil
}

func (s *PGStore) CreateInvite(ctx context.Context, inv Invite) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if inv.ClaimUserID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE invites SET used_at = NOW()
			WHERE claim_user_id = $1 AND used_at IS NULL`, inv.ClaimUserID); err != nil {
			return fmt.Errorf("revoke earlier claim invites: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invites (token_hash, role, claim_user_id, created_by, expires_at)
		VALUES ($1, $2, NULLIF($3::text, '')::uuid, NULLIF($4::text, '')::uuid, $5)`,
		inv.TokenHash, inv.Role, inv.ClaimUserID, inv.CreatedBy, inv.ExpiresAt); err != nil {
		return fmt.Errorf("insert invite: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PGStore) ListUsers(ctx context.Context) ([]UserRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userRecordCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []UserRecord{}
	for rows.Next() {
		u, err := scanUserRecord(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *PGStore) SetDisabled(ctx context.Context, userID string, disabled bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE users SET disabled_at = CASE WHEN $2::bool THEN COALESCE(disabled_at, NOW()) END
		WHERE id = $1`, userID, disabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoUser
	}
	if disabled {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		// Revoked, not just blocked: re-enabling a compromised account must not revive tokens
		// an attacker may have minted.
		if _, err := tx.Exec(ctx, `
			UPDATE api_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
			return fmt.Errorf("revoke api tokens: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE invites SET used_at = NOW() WHERE created_by = $1 AND used_at IS NULL`, userID); err != nil {
			return fmt.Errorf("revoke invites: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (s *PGStore) RebindIssuer(ctx context.Context, from, to string) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET issuer = $2 WHERE issuer = $1`, from, to)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PGStore) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= NOW()`)
	return err
}
