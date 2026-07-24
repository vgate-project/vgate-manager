// Package apple implements payment.Provider for Apple App Store In-App
// Purchases (IAP), using github.com/go-pay/gopay/apple (App Store Server API
// v2). Unlike the redirect/qr gateways, Apple IAP has no server-side payment
// URL: a native iOS app completes the purchase via StoreKit and then posts the
// signed transaction JWS to the backend, which verifies it cryptographically.
//
// Two verification entry points are provided:
//   - VerifyTransaction (used by POST /user/orders/:id/apple-verify, called
//     from the app with the signed transaction) — the primary purchase path.
//   - VerifyNotify (used by the App Store Server Notification V2 webhook at
//     /api/v1/billing/apple/notify) — handles subscription renewals.
//
// Credentials (issuer id, key id, .p8 private key, bundle id) are read lazily
// from SystemConfig (apple.* keys) through the injected ConfigSource.
package apple

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/go-pay/gopay/apple"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
)

// Platform is the canonical identifier stored on Order.Platform.
const Platform = model.OrderPlatformApple

// Config holds apple IAP credentials read from SystemConfig (apple.* keys).
type Config struct {
	IssuerID    string
	KeyID       string
	BundleID    string
	PrivateKey  string // contents of the .p8 auth key
	Environment string // "sandbox" | "prod"; defaults to "sandbox"
}

// Provider is the apple IAP implementation of payment.Provider.
type Provider struct {
	getConfig payment.ConfigSource
	mu        sync.Mutex
	cli       *apple.Client
	ckey      string
}

// NewProvider builds an apple Provider.
func NewProvider(getConfig payment.ConfigSource) (payment.Provider, error) {
	return &Provider{getConfig: getConfig}, nil
}

// Register wires the apple Provider into the given Registry under its platform
// name. Call this once at startup after NewRegistry.
func Register(r *payment.Registry) {
	r.Register(Platform, NewProvider)
}

// Platform implements payment.Provider.
func (p *Provider) Platform() string { return Platform }

// Mode implements payment.Provider. Apple IAP is completed inside a native app.
func (p *Provider) Mode() string { return payment.ModeIAP }

func (p *Provider) loadConfig() (Config, error) {
	m, err := p.getConfig()
	if err != nil {
		return Config{}, err
	}
	env := m["apple.environment"]
	if env == "" {
		env = "sandbox"
	}
	return Config{
		IssuerID:    m["apple.issuer_id"],
		KeyID:       m["apple.key_id"],
		BundleID:    m["apple.bundle_id"],
		PrivateKey:  m["apple.private_key"],
		Environment: env,
	}, nil
}

// IsConfigured implements payment.ConfigStatusProvider.
func (p *Provider) IsConfigured() bool {
	cfg, err := p.loadConfig()
	if err != nil {
		return false
	}
	return cfg.IssuerID != "" && cfg.KeyID != "" && cfg.PrivateKey != "" && cfg.BundleID != ""
}

// client returns a cached apple client, rebuilding it when the config signature
// changes. The client is only needed for App Store Server API calls; pure JWS
// verification (DecodeSignedTransaction / DecodeSignedPayload) does not require
// it.
func (p *Provider) client() (*apple.Client, error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	if !p.IsConfigured() {
		return nil, errors.New("apple is not configured")
	}
	key := cfg.IssuerID + "|" + cfg.KeyID + "|" + cfg.BundleID + "|" + cfg.Environment
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cli != nil && p.ckey == key {
		return p.cli, nil
	}
	c, err := apple.NewClient(cfg.IssuerID, cfg.BundleID, cfg.KeyID, cfg.PrivateKey, cfg.Environment == "prod")
	if err != nil {
		return nil, err
	}
	p.cli = c
	p.ckey = key
	return c, nil
}

// PayURL implements payment.Provider. Apple IAP has no server URL: the native
// app performs the purchase, so we return an "iap" directive with no URL. The
// frontend renders an "complete in the app" prompt instead of opening a link.
func (p *Provider) PayURL(order *model.Order, subject string) (*payment.PayDirective, error) {
	if !p.IsConfigured() {
		return nil, errors.New("apple is not configured")
	}
	return &payment.PayDirective{Kind: payment.ModeIAP, URL: ""}, nil
}

// VerifyTransaction verifies a signed App Store transaction JWS (posted by the
// native app after a purchase) and returns the original transaction id and the
// purchased product id. It also confirms the transaction belongs to our bundle.
func (p *Provider) VerifyTransaction(jws string) (originalTxnID, productID string, ok bool, err error) {
	if jws == "" {
		return "", "", false, errors.New("apple: empty transaction")
	}
	cfg, err := p.loadConfig()
	if err != nil {
		return "", "", false, err
	}
	st := apple.SignedTransaction(jws)
	ti, err := st.DecodeSignedTransaction()
	if err != nil {
		return "", "", false, err
	}
	// Guard against tokens issued for a different app.
	if ti.BundleId != "" && cfg.BundleID != "" && ti.BundleId != cfg.BundleID {
		return "", "", false, errors.New("apple: transaction bundle id mismatch")
	}
	return ti.OriginalTransactionId, ti.ProductId, true, nil
}

// HandleServerNotification decodes an App Store Server Notification V2 payload
// and returns the original transaction id and the notification type. It is
// used by the webhook at /api/v1/billing/apple/notify.
func (p *Provider) HandleServerNotification(body []byte) (originalTxnID, eventType string, err error) {
	var req apple.NotificationV2Req
	if err := json.Unmarshal(body, &req); err != nil {
		return "", "", err
	}
	if req.SignedPayload == "" {
		return "", "", errors.New("apple: empty signedPayload")
	}
	payload, err := apple.DecodeSignedPayload(req.SignedPayload)
	if err != nil {
		return "", "", err
	}
	originalTxnID = ""
	if payload.Data != nil && payload.Data.SignedTransactionInfo != "" {
		if ti, derr := payload.DecodeTransactionInfo(); derr == nil {
			originalTxnID = ti.OriginalTransactionId
		}
	}
	return originalTxnID, payload.NotificationType, nil
}

// VerifyNotify implements payment.Provider. It decodes the App Store Server
// Notification V2 payload and reports a successful purchase/renewal. The order
// service maps the returned original transaction id back to the user's
// subscription (see service.Reconcile). Non-purchase events are acknowledged
// (paid=false) without granting entitlement.
func (p *Provider) VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", "", false, err
	}
	originalTxnID, eventType, err := p.HandleServerNotification(body)
	if err != nil {
		return "", "", false, err
	}
	switch eventType {
	case apple.NotificationTypeV2Subscribed,
		apple.NotificationTypeV2DidRenew,
		apple.NotificationTypeV2OneTimeCharge:
		return originalTxnID, originalTxnID, originalTxnID != "", nil
	default:
		// Acknowledge everything else (revokes, expiries, pref changes, ...)
		// without flipping the order.
		return originalTxnID, originalTxnID, false, nil
	}
}
