package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Optional two-factor auth (TOTP, RFC 6238) — implemented with the standard library
// (HMAC-SHA1 + base32), no third-party dependency. 6 digits, 30-second period,
// validated with ±1 step of clock skew. The shared secret is stored per user and
// 2FA only switches on after the user proves a working code at enrollment.

var (
	ErrTOTPRequired = errors.New("two-factor authentication code required")
	ErrTOTPInvalid  = errors.New("invalid authentication code")
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func generateTOTPSecret() (string, error) {
	b := make([]byte, 20) // 160-bit secret (RFC 4226 recommendation)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// totpCodeAt computes the 6-digit code for a secret at a point in time.
func totpCodeAt(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.Unix())/30)
	h := hmac.New(sha1.New, key)
	h.Write(buf[:])
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	val := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", val%1_000_000), nil
}

// validateTOTP checks a code against the current 30s window ± one step (skew
// tolerance), using a constant-time compare.
func validateTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 || secret == "" {
		return false
	}
	now := time.Now()
	for _, skew := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		if c, err := totpCodeAt(secret, now.Add(skew)); err == nil && hmac.Equal([]byte(c), []byte(code)) {
			return true
		}
	}
	return false
}

// totpURI builds the otpauth:// URI an authenticator app imports (the user scans a
// QR of this, or enters the secret manually).
func totpURI(secret, account, issuer string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// TOTPEnabled reports whether a user has 2FA active.
func (s *Service) TOTPEnabled(id int64) bool {
	var enabled int
	s.db.QueryRow(`SELECT totp_enabled FROM users WHERE id=?`, id).Scan(&enabled) //nolint:errcheck
	return enabled != 0
}

// BeginTOTPEnroll generates and stores a fresh (still-disabled) secret and returns
// it plus the otpauth URI. Refuses when 2FA is already on — the user must disable
// (which requires a valid code) before re-enrolling, so a hijacked session can't
// silently swap the secret.
func (s *Service) BeginTOTPEnroll(id int64) (secret, uri string, err error) {
	if s.TOTPEnabled(id) {
		return "", "", errors.New("two-factor is already enabled — disable it first to re-enroll")
	}
	secret, err = generateTOTPSecret()
	if err != nil {
		return "", "", err
	}
	if _, err = s.db.Exec(`UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?`, secret, id); err != nil {
		return "", "", err
	}
	u, err := s.getUserByID(id)
	if err != nil {
		return "", "", err
	}
	account := u.Email
	if account == "" {
		account = u.Username
	}
	return secret, totpURI(secret, account, "Rigger"), nil
}

// EnableTOTP turns 2FA on after verifying a code against the enrolled secret.
func (s *Service) EnableTOTP(id int64, code string) error {
	var secret string
	if err := s.db.QueryRow(`SELECT totp_secret FROM users WHERE id=?`, id).Scan(&secret); err != nil {
		return err
	}
	if secret == "" {
		return errors.New("start two-factor enrollment first")
	}
	if !validateTOTP(secret, code) {
		return ErrTOTPInvalid
	}
	_, err := s.db.Exec(`UPDATE users SET totp_enabled=1 WHERE id=?`, id)
	return err
}

// DisableTOTP turns 2FA off, requiring a valid current code so a hijacked session
// can't drop the second factor.
func (s *Service) DisableTOTP(id int64, code string) error {
	var secret string
	var enabled int
	if err := s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE id=?`, id).Scan(&secret, &enabled); err != nil {
		return err
	}
	if enabled == 0 {
		return nil
	}
	if !validateTOTP(secret, code) {
		return ErrTOTPInvalid
	}
	_, err := s.db.Exec(`UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?`, id)
	return err
}

// checkTOTPForLogin enforces the second factor during login: returns ErrTOTPRequired
// when 2FA is on but no code was supplied, ErrTOTPInvalid when the code is wrong, nil
// otherwise (including when 2FA is off).
func (s *Service) checkTOTPForLogin(id int64, code string) error {
	var secret string
	var enabled int
	if err := s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE id=?`, id).Scan(&secret, &enabled); err != nil {
		return nil // unreadable → don't block; password already verified
	}
	if enabled == 0 {
		return nil
	}
	if strings.TrimSpace(code) == "" {
		return ErrTOTPRequired
	}
	if !validateTOTP(secret, code) {
		return ErrTOTPInvalid
	}
	return nil
}
