// Package paypal implements payment.Provider for PayPal Checkout (Orders v2,
// intent=CAPTURE). PayURL creates a hosted order and returns its "approve"
// link as a "redirect" PayDirective that the frontend opens in a browser.
// Async notifications are PayPal webhooks: the signature is verified with
// VerifyWebhookSignature and a completed capture grants entitlement.
// Credentials are read lazily from SystemConfig (paypal.* keys) through the
// injected ConfigSource.
package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/paypal"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
)

// Platform is the canonical identifier stored on Order.Platform.
const Platform = model.OrderPlatformPaypal

// Config holds paypal credentials read from SystemConfig (paypal.* keys).
type Config struct {
	ClientID   string
	Secret     string
	NotifyURL  string
	WebhookID  string
	SuccessURL string
	CancelURL  string
	Sandbox    bool
	Currency   string // ISO currency, e.g. "usd"; defaults to "usd"
}

// Provider is the paypal implementation of payment.Provider.
type Provider struct {
	getConfig payment.ConfigSource
	mu        sync.Mutex
	cli       *paypal.Client
	ckey      string
}

// NewProvider builds a paypal Provider. The paypal client is created lazily on
// first use and cached.
func NewProvider(getConfig payment.ConfigSource) (payment.Provider, error) {
	return &Provider{getConfig: getConfig}, nil
}

// Register wires the paypal Provider into the given Registry under its
// platform name. Call this once at startup after NewRegistry.
func Register(r *payment.Registry) {
	r.Register(Platform, NewProvider)
}

// Platform implements payment.Provider.
func (p *Provider) Platform() string { return Platform }

// Mode implements payment.Provider. PayPal uses a hosted checkout redirect.
func (p *Provider) Mode() string { return payment.ModeRedirect }

func (p *Provider) loadConfig() (Config, error) {
	m, err := p.getConfig()
	if err != nil {
		return Config{}, err
	}
	currency := m["paypal.currency"]
	if currency == "" {
		currency = "usd"
	}
	return Config{
		ClientID:   m["paypal.client_id"],
		Secret:     m["paypal.secret"],
		NotifyURL:  m["paypal.notify_url"],
		WebhookID:  m["paypal.webhook_id"],
		SuccessURL: m["paypal.success_url"],
		CancelURL:  m["paypal.cancel_url"],
		Sandbox:    m["paypal.sandbox"] == "true",
		Currency:   currency,
	}, nil
}

// IsConfigured implements payment.ConfigStatusProvider.
func (p *Provider) IsConfigured() bool {
	cfg, err := p.loadConfig()
	if err != nil {
		return false
	}
	return cfg.ClientID != "" && cfg.Secret != "" && cfg.NotifyURL != "" && cfg.WebhookID != "" &&
		cfg.SuccessURL != "" && cfg.CancelURL != ""
}

// client returns a cached paypal client, rebuilding it when the config
// signature changes.
func (p *Provider) client() (*paypal.Client, error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	if !p.IsConfigured() {
		return nil, errors.New("paypal is not configured")
	}
	key := cfg.ClientID + "|" + cfg.Secret + "|" + strconv.FormatBool(cfg.Sandbox)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cli != nil && p.ckey == key {
		return p.cli, nil
	}
	c, err := paypal.NewClient(cfg.ClientID, cfg.Secret, !cfg.Sandbox)
	if err != nil {
		return nil, err
	}
	p.cli = c
	p.ckey = key
	return c, nil
}

// PayURL implements payment.Provider. It creates a one-time CAPTURE order for
// order.Amount (cents, converted to the currency's major unit) and returns the
// approval link as a redirect directive. The order's OutTradeNo is stored in
// the purchase unit's custom_id so the webhook can map the capture back.
func (p *Provider) PayURL(order *model.Order, subject string) (*payment.PayDirective, error) {
	c, err := p.client()
	if err != nil {
		return nil, err
	}
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	bm := make(gopay.BodyMap)
	bm.Set("intent", "CAPTURE")
	bm.SetBodyMap("application_context", func(b gopay.BodyMap) {
		b.Set("return_url", cfg.SuccessURL)
		b.Set("cancel_url", cfg.CancelURL)
	})
	bm.SetBodyMap("purchase_units", func(b gopay.BodyMap) {
		b.Set("description", subject)
		b.Set("custom_id", order.OutTradeNo)
		b.SetBodyMap("amount", func(b gopay.BodyMap) {
			b.Set("currency_code", cfg.Currency)
			b.Set("value", centsToCurrency(order.Amount))
		})
	})
	rsp, err := c.CreateOrder(context.Background(), bm)
	if err != nil {
		return nil, err
	}
	if rsp.Code != 0 {
		return nil, fmt.Errorf("paypal create order failed: %s", rsp.Error)
	}
	for _, link := range rsp.Response.Links {
		if link.Rel == "approve" {
			return &payment.PayDirective{Kind: payment.ModeRedirect, URL: link.Href}, nil
		}
	}
	return nil, errors.New("paypal: no approve link in create order response")
}

// paypalWebhook is the envelope PayPal POSTs to the webhook URL.
type paypalWebhook struct {
	EventType string          `json:"event_type"`
	Resource  json.RawMessage `json:"resource"`
}

// paypalResource is the subset of fields we read from a webhook resource.
type paypalResource struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	CustomID      string `json:"custom_id"`
	InvoiceID     string `json:"invoice_id"`
	PurchaseUnits []struct {
		CustomID  string `json:"custom_id"`
		InvoiceID string `json:"invoice_id"`
	} `json:"purchase_units"`
}

// VerifyNotify implements payment.Provider. It verifies the webhook signature
// and, on a completed capture, returns the out_trade_no (our custom_id) and
// the capture id.
func (p *Provider) VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return "", "", false, err
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", "", false, err
	}
	bm := make(gopay.BodyMap)
	bm.Set("auth_algo", r.Header.Get("PAYPAL-AUTH-ALGO"))
	bm.Set("cert_url", r.Header.Get("PAYPAL-CERT-URL"))
	bm.Set("transmission_id", r.Header.Get("PAYPAL-TRANSMISSION-ID"))
	bm.Set("transmission_sig", r.Header.Get("PAYPAL-TRANSMISSION-SIG"))
	bm.Set("transmission_time", r.Header.Get("PAYPAL-TRANSMISSION-TIME"))
	bm.Set("webhook_id", cfg.WebhookID)
	bm.Set("webhook_event", string(body))

	c, err := p.client()
	if err != nil {
		return "", "", false, err
	}
	res, err := c.VerifyWebhookSignature(ctx, bm)
	if err != nil {
		return "", "", false, err
	}
	if res == nil || res.VerificationStatus != "SUCCESS" {
		return "", "", false, errors.New("paypal webhook signature verification failed")
	}

	var wh paypalWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return "", "", false, err
	}
	// Only a completed capture grants entitlement; other events (e.g. order
	// approved, capture denied) are acknowledged without flipping the order.
	if wh.EventType != "PAYMENT.CAPTURE.COMPLETED" {
		return "", "", false, nil
	}
	var resrc paypalResource
	if err := json.Unmarshal(wh.Resource, &resrc); err != nil {
		return "", "", false, err
	}
	outTradeNo = resrc.CustomID
	if outTradeNo == "" && len(resrc.PurchaseUnits) > 0 {
		outTradeNo = resrc.PurchaseUnits[0].CustomID
	}
	if outTradeNo == "" {
		outTradeNo = resrc.InvoiceID
	}
	tradeNo = resrc.ID
	if tradeNo == "" {
		tradeNo = outTradeNo
	}
	return outTradeNo, tradeNo, outTradeNo != "", nil
}

// centsToCurrency formats a cents amount as a currency-major-unit string with
// two decimals (e.g. 1099 -> "10.99"), as required by PayPal's value field.
func centsToCurrency(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64)
}
