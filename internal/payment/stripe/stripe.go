// Package stripe implements payment.Provider for Stripe Checkout (one-time
// payment mode). PayURL creates a hosted Checkout Session and returns its URL
// as a "redirect" PayDirective that the frontend opens in a browser. Async
// notifications are Stripe webhooks; the signature is verified with the webhook
// signing secret, and a completed checkout.session event grants entitlement.
// Credentials are read lazily from SystemConfig (stripe.* keys) through the
// injected ConfigSource.
package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/webhook"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
)

// Platform is the canonical identifier stored on Order.Platform.
const Platform = model.OrderPlatformStripe

// eventCheckoutCompleted is the Stripe webhook event that grants entitlement.
const eventCheckoutCompleted = "checkout.session.completed"

// Config holds stripe credentials read from SystemConfig (stripe.* keys).
type Config struct {
	SecretKey     string
	WebhookSecret string
	SuccessURL    string
	CancelURL     string
	Currency      string // ISO currency, e.g. "cny"; defaults to "cny"
}

// Provider is the stripe implementation of payment.Provider.
type Provider struct {
	getConfig payment.ConfigSource
}

// NewProvider builds a stripe Provider.
func NewProvider(getConfig payment.ConfigSource) (payment.Provider, error) {
	return &Provider{getConfig: getConfig}, nil
}

// Register wires the stripe Provider into the given Registry under its platform
// name. Call this once at startup after NewRegistry.
func Register(r *payment.Registry) {
	r.Register(Platform, NewProvider)
}

// Platform implements payment.Provider.
func (p *Provider) Platform() string { return Platform }

func (p *Provider) loadConfig() (Config, error) {
	m, err := p.getConfig()
	if err != nil {
		return Config{}, err
	}
	currency := m["stripe.currency"]
	if currency == "" {
		currency = "cny"
	}
	return Config{
		SecretKey:     m["stripe.secret_key"],
		WebhookSecret: m["stripe.webhook_secret"],
		SuccessURL:    m["stripe.success_url"],
		CancelURL:     m["stripe.cancel_url"],
		Currency:      currency,
	}, nil
}

// PayURL implements payment.Provider. It creates a hosted Checkout Session for
// a one-time payment of order.Amount (already in the currency's minor unit,
// e.g. cents for cny) and returns the session URL as a redirect directive.
func (p *Provider) PayURL(order *model.Order, subject string) (*payment.PayDirective, error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.SecretKey == "" || cfg.SuccessURL == "" || cfg.CancelURL == "" {
		return nil, errors.New("stripe is not configured")
	}
	stripe.Key = cfg.SecretKey
	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String("payment"),
		SuccessURL:        stripe.String(cfg.SuccessURL),
		CancelURL:         stripe.String(cfg.CancelURL),
		ClientReferenceID: stripe.String(order.OutTradeNo),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency:   stripe.String(cfg.Currency),
					UnitAmount: stripe.Int64(order.Amount),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String(subject),
					},
				},
				Quantity: stripe.Int64(1),
			},
		},
	}
	sess, err := session.New(params)
	if err != nil {
		return nil, err
	}
	return &payment.PayDirective{Kind: "redirect", URL: sess.URL}, nil
}

// VerifyNotify implements payment.Provider. It verifies the webhook signature
// with the signing secret and, on a completed checkout session, returns the
// out_trade_no (our ClientReferenceID) and the session id.
func (p *Provider) VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return "", "", false, err
	}
	if cfg.WebhookSecret == "" {
		return "", "", false, errors.New("stripe is not configured")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", "", false, err
	}
	event, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), cfg.WebhookSecret)
	if err != nil {
		return "", "", false, err
	}
	if event.Type != eventCheckoutCompleted {
		// Acknowledge any other event type without granting entitlement.
		return "", "", false, nil
	}
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		return "", "", false, err
	}
	return sess.ClientReferenceID, sess.ID, true, nil
}
