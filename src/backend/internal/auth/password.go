package auth

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Password policy — configurable via app_settings, enforced at every site that
// sets a password (first-run setup, invite completion, admin reset, self-service
// change, and the forgot-password reset flow). Defaults match the historical
// 8-character minimum so existing installs see no behaviour change.

const (
	defaultPwMinLength = 8
	pwMinFloor         = 6 // never enforce a configured minimum below this
)

// PasswordPolicy is the resolved policy the UI displays and the server enforces.
type PasswordPolicy struct {
	MinLength     int  `json:"min_length"`
	RequireUpper  bool `json:"require_upper"`
	RequireLower  bool `json:"require_lower"`
	RequireNumber bool `json:"require_number"`
	RequireSymbol bool `json:"require_symbol"`
}

// authSetting reads a global app_settings value ("" when unset).
func (s *Service) authSetting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&v) //nolint:errcheck
	return v
}

func settingTrue(v string) bool { return v == "true" || v == "1" }

// PasswordPolicy resolves the active policy from app_settings (with defaults).
func (s *Service) PasswordPolicy() PasswordPolicy {
	p := PasswordPolicy{MinLength: defaultPwMinLength}
	if n, err := strconv.Atoi(strings.TrimSpace(s.authSetting("pw_min_length"))); err == nil && n > 0 {
		p.MinLength = n
	}
	if p.MinLength < pwMinFloor {
		p.MinLength = pwMinFloor
	}
	p.RequireUpper = settingTrue(s.authSetting("pw_require_upper"))
	p.RequireLower = settingTrue(s.authSetting("pw_require_lower"))
	p.RequireNumber = settingTrue(s.authSetting("pw_require_number"))
	p.RequireSymbol = settingTrue(s.authSetting("pw_require_symbol"))
	return p
}

// ValidatePassword enforces the active policy on a candidate password.
func (s *Service) ValidatePassword(pw string) error {
	p := s.PasswordPolicy()
	if len(pw) < p.MinLength {
		return fmt.Errorf("password must be at least %d characters", p.MinLength)
	}
	var hasUpper, hasLower, hasNumber, hasSymbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsNumber(r):
			hasNumber = true
		case unicode.IsPunct(r), unicode.IsSymbol(r):
			hasSymbol = true
		}
	}
	var missing []string
	if p.RequireUpper && !hasUpper {
		missing = append(missing, "an uppercase letter")
	}
	if p.RequireLower && !hasLower {
		missing = append(missing, "a lowercase letter")
	}
	if p.RequireNumber && !hasNumber {
		missing = append(missing, "a number")
	}
	if p.RequireSymbol && !hasSymbol {
		missing = append(missing, "a symbol")
	}
	if len(missing) > 0 {
		return errors.New("password must include " + strings.Join(missing, ", "))
	}
	return nil
}
