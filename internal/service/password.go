package service

import (
	"errors"
	"fmt"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword is the package-level bcrypt helper used by the user service.
// (AuthService keeps its own method for API symmetry.)
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// Validate enforces the password policy: a minimum length, and — when
// RequireComplexity is set — at least one lowercase letter, one uppercase
// letter, and one digit.
func (p PasswordPolicy) Validate(pwd string) error {
	if len(pwd) < p.MinLength {
		return fmt.Errorf("password must be at least %d characters", p.MinLength)
	}
	if p.RequireComplexity && !hasComplexity(pwd) {
		return errors.New("password must contain uppercase, lowercase, and a digit")
	}
	return nil
}

// hasComplexity reports whether s contains at least one lowercase letter, one
// uppercase letter, and one digit.
func hasComplexity(s string) bool {
	var lower, upper, digit bool
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return lower && upper && digit
}
