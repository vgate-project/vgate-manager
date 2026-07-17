// Package util provides small ID and secret-generation helpers.
package util

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
)

// NewNodeID returns a 26-char ULID for a Node primary key.
func NewNodeID() string {
	return ulid.Make().String()
}

// NewUserID returns a UUID string — the user's stable primary key.
func NewUserID() string {
	return uuid.NewString()
}

// NewCredential returns a UUID string used as the rotatable VLESS credential.
// It is deliberately distinct from the user's primary key (NewUserID) so a
// leaked credential can be regenerated without changing the primary key.
func NewCredential() string {
	return uuid.NewString()
}

// NewPlanID returns a ULID for a Plan primary key.
func NewPlanID() string {
	return ulid.Make().String()
}

// NewPlanPriceID returns a ULID for a PlanPrice primary key.
func NewPlanPriceID() string {
	return ulid.Make().String()
}

// NewTrafficPackageID returns a ULID for a TrafficPackage primary key.
func NewTrafficPackageID() string {
	return ulid.Make().String()
}

// NewOrderID returns a ULID for an Order primary key.
func NewOrderID() string {
	return ulid.Make().String()
}

// NewInviteID returns a ULID for an InviteCode primary key.
func NewInviteID() string {
	return ulid.Make().String()
}

// NewVerificationID returns a ULID for an EmailVerification primary key.
func NewVerificationID() string {
	return ulid.Make().String()
}

// NewAnnouncementID returns a ULID for an Announcement primary key.
func NewAnnouncementID() string {
	return ulid.Make().String()
}

// NewRedemptionID returns a ULID for a RedemptionCode primary key.
func NewRedemptionID() string {
	return ulid.Make().String()
}

// NewTicketID returns a ULID for a Ticket primary key.
func NewTicketID() string {
	return ulid.Make().String()
}

// NewTicketMessageID returns a ULID for a TicketMessage primary key.
func NewTicketMessageID() string {
	return ulid.Make().String()
}

// NewRedemptionCode returns a URL-safe shareable redemption code (24 hex chars,
// no ambiguous characters). RandomToken already uses crypto/rand.
func NewRedemptionCode() string {
	return RandomToken(12)
}

// NewInviteCode returns a URL-safe shareable invite code (16 hex chars, no
// ambiguous characters). RandomToken already uses crypto/rand.
func NewInviteCode() string {
	return RandomToken(8)
}

// RandomToken returns a hex-encoded crypto-random string (byteLen*2 chars).
// Use 32 bytes for node tokens (256-bit), 16 bytes for sub/refresh tokens (128-bit).
func RandomToken(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
