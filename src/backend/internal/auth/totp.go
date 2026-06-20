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

// EnableTOTP turns 2FA on after verifying a code against the enrolled secret, then
// generates and returns a fresh set of single-use recovery codes (shown to the user
// once — only their hashes are stored).
func (s *Service) EnableTOTP(id int64, code string) ([]string, error) {
	var secret string
	if err := s.db.QueryRow(`SELECT totp_secret FROM users WHERE id=?`, id).Scan(&secret); err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, errors.New("start two-factor enrollment first")
	}
	if !validateTOTP(secret, code) {
		return nil, ErrTOTPInvalid
	}
	if _, err := s.db.Exec(`UPDATE users SET totp_enabled=1 WHERE id=?`, id); err != nil {
		return nil, err
	}
	return s.resetRecoveryCodes(id)
}

// DisableTOTP turns 2FA off (and discards recovery codes), requiring a valid current
// code (TOTP or a recovery code) so a hijacked session can't drop the second factor.
func (s *Service) DisableTOTP(id int64, code string) error {
	var secret string
	var enabled int
	if err := s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE id=?`, id).Scan(&secret, &enabled); err != nil {
		return err
	}
	if enabled == 0 {
		return nil
	}
	if !validateTOTP(secret, code) && !s.consumeRecoveryCode(id, code) {
		return ErrTOTPInvalid
	}
	if _, err := s.db.Exec(`UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM user_recovery_codes WHERE user_id=?`, id)
	return err
}

// RegenerateRecoveryCodes issues a new set (invalidating the old), gated on a valid
// current TOTP or recovery code. Returns the new plaintext codes (shown once).
func (s *Service) RegenerateRecoveryCodes(id int64, code string) ([]string, error) {
	var secret string
	var enabled int
	if err := s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE id=?`, id).Scan(&secret, &enabled); err != nil {
		return nil, err
	}
	if enabled == 0 {
		return nil, errors.New("two-factor is not enabled")
	}
	if !validateTOTP(secret, code) && !s.consumeRecoveryCode(id, code) {
		return nil, ErrTOTPInvalid
	}
	return s.resetRecoveryCodes(id)
}

// RecoveryCodeCount returns how many unused recovery codes remain.
func (s *Service) RecoveryCodeCount(id int64) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(1) FROM user_recovery_codes WHERE user_id=? AND used_at IS NULL`, id).Scan(&n) //nolint:errcheck
	return n
}

// checkTOTPForLogin enforces the second factor during login: returns ErrTOTPRequired
// when 2FA is on but no code was supplied, ErrTOTPInvalid when neither the TOTP code
// nor a recovery code matches, nil otherwise (including when 2FA is off). A matching
// recovery code is consumed.
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
	if validateTOTP(secret, code) {
		return nil
	}
	if s.consumeRecoveryCode(id, code) {
		return nil
	}
	return ErrTOTPInvalid
}

// ── Recovery (backup) codes ─────────────────────────────────────────────────────

const recoveryCodeCount = 10

// generateRecoveryCodes returns N random, human-readable single-use codes
// (e.g. "ABCDE-FGHIJ" from the base32 alphabet).
func generateRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		raw := strings.ToUpper(b32.EncodeToString(b))[:10]
		codes[i] = raw[:5] + "-" + raw[5:]
	}
	return codes, nil
}

// normalizeRecovery strips formatting so codes hash consistently regardless of how
// the user types them (case, dashes, spaces).
func normalizeRecovery(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// resetRecoveryCodes replaces a user's recovery codes with a fresh set, returning the
// plaintext (the caller surfaces them once; only hashes are stored).
func (s *Service) resetRecoveryCodes(id int64) ([]string, error) {
	codes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM user_recovery_codes WHERE user_id=?`, id); err != nil {
		return nil, err
	}
	for _, c := range codes {
		if _, err := s.db.Exec(`INSERT INTO user_recovery_codes (user_id, code_hash) VALUES (?, ?)`, id, hashToken(normalizeRecovery(c))); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// consumeRecoveryCode marks a matching unused recovery code used; reports success.
func (s *Service) consumeRecoveryCode(id int64, code string) bool {
	n := normalizeRecovery(code)
	if n == "" {
		return false
	}
	res, err := s.db.Exec(`UPDATE user_recovery_codes SET used_at=CURRENT_TIMESTAMP WHERE user_id=? AND code_hash=? AND used_at IS NULL`, id, hashToken(n))
	if err != nil {
		return false
	}
	aff, _ := res.RowsAffected()
	return aff > 0
}
