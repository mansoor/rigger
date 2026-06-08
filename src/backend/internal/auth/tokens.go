package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// One-time tokens handed out in links (invite / email verification / password
// reset / later 2FA). Only the SHA-256 of the value is stored; the raw value
// lives only in the emailed/surfaced link.

const (
	KindInvite = "invite"
	KindVerify = "verify"
	KindReset  = "reset"
)

var ErrInvalidToken = errors.New("invalid or expired token")

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func genTokenValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// createToken issues a fresh token of kind for a user, invalidating any prior
// unused token of the same kind. Returns the raw value (for the link).
func (s *Service) createToken(userID int64, kind string, ttl time.Duration) (string, error) {
	raw, err := genTokenValue()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`DELETE FROM user_tokens WHERE user_id=? AND kind=? AND used_at IS NULL`, userID, kind); err != nil {
		return "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO user_tokens (user_id, kind, token_hash, expires_at) VALUES (?, ?, ?, ?)`,
		userID, kind, hashToken(raw), time.Now().Add(ttl).UTC(),
	)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// peekToken validates a raw token of kind without consuming it, returning the user id.
func (s *Service) peekToken(raw, kind string) (int64, error) {
	var userID int64
	var expires time.Time
	err := s.db.QueryRow(
		`SELECT user_id, expires_at FROM user_tokens WHERE token_hash=? AND kind=? AND used_at IS NULL`,
		hashToken(raw), kind,
	).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalidToken
	}
	if err != nil {
		return 0, err
	}
	if time.Now().After(expires) {
		return 0, ErrInvalidToken
	}
	return userID, nil
}

// consumeToken validates and marks a token used, returning the user id.
func (s *Service) consumeToken(raw, kind string) (int64, error) {
	userID, err := s.peekToken(raw, kind)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`UPDATE user_tokens SET used_at=CURRENT_TIMESTAMP WHERE token_hash=?`, hashToken(raw)); err != nil {
		return 0, err
	}
	return userID, nil
}
