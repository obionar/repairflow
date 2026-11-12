package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"repairflow/internal/errs"

	"golang.org/x/crypto/argon2"
)

// Local authentication with roles admin/technician/viewer.
// Dependency note: golang.org/x/crypto/argon2 is the single added dependency
// of the web stage — a memory-hard password hash is a security requirement
// (matching the Python reference's Argon2id) and cannot be reasonably
// hand-rolled.
//
// Storage rules (mirroring the Python reference): no raw passwords anywhere;
// PHC-encoded Argon2id hashes in users.password_hash; session tokens exist
// only as SHA-256 hashes; revocation is explicit and immediate.

// Role values.
const (
	RoleAdmin      = "admin"
	RoleTechnician = "technician"
	RoleViewer     = "viewer"
)

// SessionTTLS is the absolute session lifetime.
const SessionTTLS = 24 * time.Hour

// MinPasswordLength is the minimum for new or changed passwords. Existing
// hashes remain verifiable so this policy does not lock out older accounts.
// The application is local-first; operators are expected to choose longer
// passwords when the web UI is reachable from a shared network.
const MinPasswordLength = 6

var roleSet = map[string]bool{RoleAdmin: true, RoleTechnician: true, RoleViewer: true}

// CanWrite reports whether a role may perform mutations.
func CanWrite(role string) bool { return role == RoleAdmin || role == RoleTechnician }

// CanManageUsers reports whether a role may manage accounts.
func CanManageUsers(role string) bool { return role == RoleAdmin }

// CanPurge reports whether a role may perform destructive media purge:
// explicitly authorized operators only — admin).
func CanPurge(role string) bool { return role == RoleAdmin }

// User is a local operator account.
type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Session is an authenticated browser/API session (token is returned only at
// creation; storage keeps its hash).
type Session struct {
	TokenHash  string `json:"-"`
	CSRFToken  string `json:"csrf_token"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username,omitempty"`
	Role       string `json:"role,omitempty"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	LastSeenAt string `json:"last_seen_at"`
}

// argon2id parameters (OWASP-aligned; m=64MB may be heavy on SBCs, so m=32MB).
const (
	argonTime    = 3
	argonMemory  = 32 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword renders a PHC-encoded Argon2id hash.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", errs.New(errs.CodeValidation, "password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > 256 {
		return "", errs.New(errs.CodeValidation, "password must be at most 256 characters")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "cannot generate password salt")
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return "$argon2id$v=19,m=" + strconv.FormatInt(argonMemory, 10) + ",t=" + strconv.FormatInt(argonTime, 10) +
		",p=" + strconv.FormatInt(int64(argonThreads), 10) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(hash), nil
}

// CheckPassword verifies a password against a stored PHC hash in constant
// time.
func CheckPassword(stored, password string) bool {
	// PHC format: "$argon2id$v=19,...$salt$hash" -> split("$") yields
	// ["", "argon2id", "params", "salt", "hash"].
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CreateUser adds a local operator account.
func (s *Service) CreateUser(ctx context.Context, actor, username, password, role string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 100 {
		return User{}, errs.New(errs.CodeValidation, "username must be 1-100 characters")
	}
	if !roleSet[role] {
		return User{}, errs.New(errs.CodeValidation, "unknown role %q (valid: admin, technician, viewer)", role)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	ts := s.timestamp()
	var u User
	err = s.InTx(ctx, func(ctx context.Context) error {
		scanErr := s.q(ctx).QueryRowContext(ctx,
			`INSERT INTO users(username, password_hash, role, created_at, updated_at) VALUES(?,?,?,?,?)
			 RETURNING id, username, role, active, created_at, updated_at`,
			username, hash, role, ts, ts).
			Scan(&u.ID, &u.Username, &u.Role, &u.Active, &u.CreatedAt, &u.UpdatedAt)
		if scanErr != nil {
			return errs.Wrap(errs.CodeConflict, scanErr, "cannot create user %q (username may be taken)", username)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// ChangePassword updates a user's password and revokes all of their sessions
// (a password change ends existing sessions — security baseline).
func (s *Service) ChangePassword(ctx context.Context, actor, username, oldPassword, newPassword string) error {
	u, hash, err := s.userByName(ctx, username)
	if err != nil {
		return err
	}
	if !CheckPassword(hash, oldPassword) {
		return errs.New(errs.CodeValidation, "current password is incorrect")
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	ts := s.timestamp()
	return s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE users SET password_hash=?, updated_at=? WHERE id=?`, newHash, ts, u.ID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot update password")
		}
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE auth_sessions SET revoked_at=? WHERE user_id=? AND revoked_at=''`, ts, u.ID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot revoke sessions")
		}
		return nil
	})
}

// ResetPassword changes an operator's password without requiring the old
// password. callerRole must come from the authenticated session; the CLI is
// outside this authorization boundary and passes RoleAdmin explicitly.
// Every existing session for the account is revoked in the same transaction.
func (s *Service) ResetPassword(ctx context.Context, actor, callerRole, username, newPassword string) error {
	if callerRole != RoleAdmin {
		return errs.New(errs.CodePermission, "only administrators may reset operator passwords")
	}
	u, _, err := s.userByName(ctx, username)
	if err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	ts := s.timestamp()
	return s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE users SET password_hash=?, updated_at=? WHERE id=?`, hash, ts, u.ID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot reset password")
		}
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE auth_sessions SET revoked_at=? WHERE user_id=? AND revoked_at=''`, ts, u.ID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot revoke sessions")
		}
		return nil
	})
}

// SetUserActive enables or disables an operator and revokes its sessions.
// Disabling the last active administrator is refused to prevent lockout.
func (s *Service) SetUserActive(ctx context.Context, actor, callerRole, username string, active bool) (User, error) {
	if callerRole != RoleAdmin {
		return User{}, errs.New(errs.CodePermission, "only administrators may enable or disable operators")
	}
	u, _, err := s.userByName(ctx, username)
	if err != nil {
		return User{}, err
	}
	if u.Active == active {
		return User{}, errs.New(errs.CodeConflict, "user %q is already %s", username, activeLabel(active))
	}
	if !active && u.Role == RoleAdmin {
		var remaining int
		if err := s.q(ctx).QueryRowContext(ctx,
			`SELECT count(*) FROM users WHERE role = 'admin' AND active = 1 AND id <> ?`, u.ID).Scan(&remaining); err != nil {
			return User{}, errs.Wrap(errs.CodeInternal, err, "cannot count active administrators")
		}
		if remaining == 0 {
			return User{}, errs.New(errs.CodeConflict, "cannot disable the last active administrator")
		}
	}
	ts := s.timestamp()
	flag := 0
	if active {
		flag = 1
	}
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE users SET active=?, updated_at=? WHERE id=?`, flag, ts, u.ID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot change user active state")
		}
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE auth_sessions SET revoked_at=? WHERE user_id=? AND revoked_at=''`, ts, u.ID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot revoke sessions")
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	u.Active, u.UpdatedAt = active, ts
	return u, nil
}

func activeLabel(active bool) string {
	if active {
		return "active"
	}
	return "disabled"
}

// Login authenticates and opens a session. The returned session carries the
// raw token exactly once; only its hash is stored.
func (s *Service) Login(ctx context.Context, username, password string) (Session, string, error) {
	u, hash, err := s.userByName(ctx, strings.TrimSpace(username))
	if err != nil {
		if errs.CodeOf(err) == errs.CodeNotFound {
			// Uniform failure for unknown user and bad password.
			return Session{}, "", errs.New(errs.CodeValidation, "invalid username or password")
		}
		return Session{}, "", err
	}
	if !u.Active || !CheckPassword(hash, password) {
		return Session{}, "", errs.New(errs.CodeValidation, "invalid username or password")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, "", errs.Wrap(errs.CodeInternal, err, "cannot generate session token")
	}
	token := hex.EncodeToString(raw)
	tokenHash := sessionTokenHash(token)
	csrf := make([]byte, 32)
	if _, err := rand.Read(csrf); err != nil {
		return Session{}, "", errs.Wrap(errs.CodeInternal, err, "cannot generate csrf token")
	}
	ts := s.timestamp()
	expires := s.now().Add(SessionTTLS).Format(time.RFC3339)
	sess := Session{TokenHash: tokenHash, CSRFToken: hex.EncodeToString(csrf), UserID: u.ID}
	err = s.InTx(ctx, func(ctx context.Context) error {
		_, err := s.q(ctx).ExecContext(ctx,
			`INSERT INTO auth_sessions(token_hash, csrf_token, user_id, created_at, expires_at, last_seen_at)
			 VALUES(?,?,?,?,?,?)`, sess.TokenHash, sess.CSRFToken, sess.UserID, ts, expires, ts)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot create session")
		}
		return nil
	})
	if err != nil {
		return Session{}, "", err
	}
	sess.Username, sess.Role, sess.ExpiresAt, sess.CreatedAt, sess.LastSeenAt = u.Username, u.Role, expires, ts, ts
	return sess, token, nil
}

// LookupSession resolves a raw token to an active session with its user.
func (s *Service) LookupSession(ctx context.Context, token string) (Session, error) {
	var sess Session
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT s.token_hash, s.csrf_token, s.user_id, u.username, u.role, s.created_at, s.expires_at, s.last_seen_at
		 FROM auth_sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.revoked_at = '' AND u.active = 1`,
		sessionTokenHash(token)).
		Scan(&sess.TokenHash, &sess.CSRFToken, &sess.UserID, &sess.Username, &sess.Role,
			&sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt)
	if err != nil {
		return Session{}, errs.New(errs.CodeValidation, "not authenticated")
	}
	expires, err := time.Parse(time.RFC3339, sess.ExpiresAt)
	if err != nil || s.now().After(expires) {
		return Session{}, errs.New(errs.CodeValidation, "session expired")
	}
	return sess, nil
}

// Logout revokes one session immediately.
func (s *Service) Logout(ctx context.Context, token string) error {
	ts := s.timestamp()
	_, err := s.q(ctx).ExecContext(ctx,
		`UPDATE auth_sessions SET revoked_at=? WHERE token_hash=?`, ts, sessionTokenHash(token))
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot revoke session")
	}
	return nil
}

// ListUsers returns all accounts (metadata only — never password hashes).
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT id, username, role, active, created_at, updated_at FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list users")
	}
	defer func() { _ = rows.Close() }()
	out := []User{}
	for rows.Next() {
		var u User
		var active int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &active, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Active = active == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// userByName reads a user and its password hash.
func (s *Service) userByName(ctx context.Context, username string) (User, string, error) {
	var u User
	var hash string
	var active int
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, active, created_at, updated_at FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &hash, &u.Role, &active, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", errs.New(errs.CodeNotFound, "user %q does not exist", username)
	}
	if err != nil {
		return User{}, "", errs.Wrap(errs.CodeInternal, err, "cannot read user %q", username)
	}
	u.Active = active == 1
	return u, hash, nil
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SecureTokenEqual compares two tokens in constant time.
func SecureTokenEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}
