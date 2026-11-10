package auth

// Ending sessions.
//
// Rigger's sessions are signed tokens, valid on their own until they expire, so
// ending one early takes a check against the account on every use. A token
// stands only while its account is active and was issued no earlier than the
// account's last password change (password_changed_at) and last revocation
// (sessions_revoked_at, stamped when an account is disabled). So a password
// change — in the app, through a reset link, or `rigger users reset-password` —
// signs the account out everywhere, and disabling an account ends its sessions
// for good: enabling it again doesn't revive them.

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrSessionEnded means a genuine token whose session is over: the account was
// disabled or deleted, or its password changed or its sessions were revoked
// after the token was issued.
var ErrSessionEnded = errors.New("session ended")

// ErrSessionLookup wraps a database failure while checking a session. Callers
// answer it with 503, not 401: a database hiccup mustn't sign anyone out.
var ErrSessionLookup = errors.New("could not check the session")

// checkSession reports whether the session behind c still stands, and returns
// the account's current role.
//
// Times compare in whole seconds, the precision of both the token's iat and the
// database's timestamps. A token from the same second as a change is accepted:
// the session a password change hands back is issued within that second.
func (s *Service) checkSession(c *Claims) (role string, err error) {
	if c.IssuedAt == nil {
		return "", ErrSessionEnded
	}
	var status string
	var changed, revoked int64
	err = s.db.QueryRow(`SELECT status, role,
		COALESCE(CAST(strftime('%s', password_changed_at) AS INTEGER), 0),
		COALESCE(CAST(strftime('%s', sessions_revoked_at) AS INTEGER), 0)
		FROM users WHERE id=?`, c.UserID).Scan(&status, &role, &changed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionEnded
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSessionLookup, err)
	}
	if status != StatusActive {
		return "", ErrSessionEnded
	}
	if iat := c.IssuedAt.Unix(); iat < changed || iat < revoked {
		return "", ErrSessionEnded
	}
	return role, nil
}

// SessionActive reports whether the session behind c still stands. Long-lived
// streams — terminals, log and deploy streams, the live-events feed — were
// authorized once, when they opened; they call this periodically and end when it
// turns false. A failed lookup counts as active, so a database hiccup doesn't cut
// a running stream.
func (s *Service) SessionActive(c *Claims) bool {
	_, err := s.checkSession(c)
	return !errors.Is(err, ErrSessionEnded)
}
