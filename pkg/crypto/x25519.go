// Package crypto provides small X25519 helpers for Reality / v2 key handling.
// It uses the standard library crypto/ecdh — no xray-core or xtls/reality dep.
package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateX25519KeyPair returns a fresh (private, public) key pair as base64url
// strings (no padding). Used to mint Reality server private keys.
func GenerateX25519KeyPair() (privB64, pubB64 string, err error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate x25519 key: %w", err)
	}
	privB64 = base64.RawURLEncoding.EncodeToString(priv.Bytes())
	pubB64 = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	return privB64, pubB64, nil
}

// DeriveX25519Public returns the base64url public key for a base64url private
// key. Used to derive Reality pbk / v2 pbk for subscription links.
func DeriveX25519Public(privB64 string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(privB64)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("invalid x25519 private key length: %d", len(raw))
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}
