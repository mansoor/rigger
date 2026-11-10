package auth

// Account recovery from the server console.
//
// The web recovery paths both assume something still works: forgot-password
// needs a configured mailer, and the Users page needs another administrator who
// can sign in. When the only administrator is locked out, neither does, and the
// fallback was editing the database by hand.
//
// These methods back `rigger users …`, which runs inside the container. Their
// caller has shell access to the host — the same trust as editing the database —
// so they skip the proofs a browser must give (the current password, a TOTP
// code). Everything else is the same as the web flows: the password policy,
// bcrypt, and the single-use reset tokens the /reset-password page consumes.

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Bounds for a console-issued reset link. The forgot-password email uses one
// hour; an operator handing a link to someone may need longer, but a reset link
// is a credential, so not indefinitely.
const (
	MinResetLinkTTL = 5 * time.Minute
	MaxResetLinkTTL = 72 * time.Hour
	// DefaultResetLinkTTL matches the forgot-password email.
	DefaultResetLinkTTL = resetTTL
)

// ErrAccountInvited is returned when a reset link is requested for an account
// that has not accepted its invitation: the reset page does not activate
// accounts, so the link would set a password the user still could not sign in
// with.
var ErrAccountInvited = errors.New("the account has not accepted its invitation yet")

// ErrAccountDisabled is returned when a reset link is requested for a disabled
// account, which could not sign in with the new password either.
var ErrAccountDisabled = errors.New("the account is disabled")

// FindUser resolves an account by email (case-insensitive) or numeric id.
func (s *Service) FindUser(ref string) (*UserInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ErrUserNotFound
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return s.getUserByID(id)
	}
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email=? LIMIT 1`,
		strings.ToLower(ref)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// SetPasswordFromConsole sets a password without knowing the current one. It is
// held to the configured password policy, and records the change time, which
// ends every session the account had. An account that has not accepted its
// invitation is activated, since the operator is setting its first password;
// activated reports whether that happened. A disabled account stays disabled —
// enabling it is a separate, deliberate step.
func (s *Service) SetPasswordFromConsole(id int64, password string) (activated bool, err error) {
	u, err := s.getUserByID(id)
	if err != nil {
		return false, err
	}
	if err := s.ValidatePassword(password); err != nil {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	status := u.Status
	if status == StatusInvited {
		status = StatusActive
	}
	if _, err := s.db.Exec(
		`UPDATE users SET password=?, status=?, password_changed_at=CURRENT_TIMESTAMP WHERE id=?`,
		string(hash), status, id); err != nil {
		return false, err
	}
	activated = u.Status == StatusInvited
	if activated {
		// The account's invitation link would otherwise still work, letting
		// whoever holds that email set a different password over this one.
		if _, err := s.db.Exec(`DELETE FROM user_tokens WHERE user_id=? AND kind=? AND used_at IS NULL`,
			id, KindInvite); err != nil {
			return activated, err
		}
	}
	return activated, nil
}

// ResetTOTP turns off two-factor authentication and deletes the recovery codes,
// for an account whose authenticator is lost. Unlike DisableTOTP it needs no
// code — the console is the proof of authority. wasEnabled reports whether 2FA
// had been on.
func (s *Service) ResetTOTP(id int64) (wasEnabled bool, err error) {
	if _, err := s.getUserByID(id); err != nil {
		return false, err
	}
	wasEnabled = s.TOTPEnabled(id)
	if _, err := s.db.Exec(`UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?`, id); err != nil {
		return false, err
	}
	_, err = s.db.Exec(`DELETE FROM user_recovery_codes WHERE user_id=?`, id)
	return wasEnabled, err
}

// CreateResetLinkToken mints a password-reset token for an active account. It is
// the same single-use token the forgot-password email carries, so the browser
// completes the reset on the existing /reset-password page. Issuing one cancels
// any earlier unused reset token for the account.
func (s *Service) CreateResetLinkToken(id int64, ttl time.Duration) (string, error) {
	u, err := s.getUserByID(id)
	if err != nil {
		return "", err
	}
	switch u.Status {
	case StatusActive:
	case StatusDisabled:
		return "", ErrAccountDisabled
	default:
		return "", ErrAccountInvited
	}
	if ttl < MinResetLinkTTL || ttl > MaxResetLinkTTL {
		return "", fmt.Errorf("link lifetime must be between %s and %s", MinResetLinkTTL, MaxResetLinkTTL)
	}
	return s.createToken(id, KindReset, ttl)
}

// Character classes for generated passwords. Quotes, backslashes and spaces are
// left out so a password survives being pasted anywhere.
const (
	pwLower  = "abcdefghijkmnopqrstuvwxyz" // no l, which reads as 1
	pwUpper  = "ABCDEFGHJKLMNPQRSTUVWXYZ"  // no I or O
	pwDigit  = "23456789"                  // no 0 or 1
	pwSymbol = "!@#%^&*-_=+"
)

// generatedPasswordMin is the length of a generated password unless the policy
// demands more: about 120 bits of entropy from this alphabet.
const generatedPasswordMin = 20

// GeneratePassword returns a random password that satisfies the current policy.
// It always includes every character class, so it passes whatever combination of
// requirements an administrator has switched on, and it is checked against the
// policy before being returned.
func (s *Service) GeneratePassword() (string, error) {
	length := generatedPasswordMin
	if p := s.PasswordPolicy(); p.MinLength > length {
		length = p.MinLength
	}
	all := pwLower + pwUpper + pwDigit + pwSymbol
	out := make([]byte, 0, length)
	for _, class := range []string{pwLower, pwUpper, pwDigit, pwSymbol} {
		c, err := randomChar(class)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < length {
		c, err := randomChar(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	// Shuffle so the guaranteed characters are not always in front.
	for i := len(out) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		out[i], out[j.Int64()] = out[j.Int64()], out[i]
	}
	pw := string(out)
	if err := s.ValidatePassword(pw); err != nil {
		return "", fmt.Errorf("generated password does not satisfy the policy: %w", err)
	}
	return pw, nil
}

func randomChar(alphabet string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	if err != nil {
		return 0, err
	}
	return alphabet[n.Int64()], nil
}
