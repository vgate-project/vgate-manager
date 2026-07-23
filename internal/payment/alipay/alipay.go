// Package alipay implements payment.Provider for Alipay's offline QR pre-creation
// (alipay.trade.precreate, 统一收单线下交易预创建) and async notification
// verification. PayURL returns a "qr" PayDirective whose URL is the pre-created
// QR code string the user scans to pay. Credentials are read lazily from
// SystemConfig (alipay.* keys) through the injected ConfigSource, and the alipay
// client is cached and rebuilt only when the config signature changes.
package alipay

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/smartwalle/alipay/v3"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
)

// Platform is the canonical identifier stored on Order.Platform.
const Platform = model.OrderPlatformAlipay

// Config holds alipay credentials read from SystemConfig (alipay.* keys).
type Config struct {
	AppID      string
	PrivateKey string
	PublicKey  string
	NotifyURL  string
	Sandbox    bool
}

// Provider is the alipay implementation of payment.Provider.
type Provider struct {
	getConfig payment.ConfigSource
	mu        sync.RWMutex
	cache     *alipay.Client
	ckey      string
}

// NewProvider builds an alipay Provider. The alipay client is created lazily
// on first use and cached.
func NewProvider(getConfig payment.ConfigSource) (payment.Provider, error) {
	return &Provider{getConfig: getConfig}, nil
}

// Register wires the alipay Provider into the given Registry under its
// platform name. Call this once at startup after NewRegistry.
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
	return Config{
		AppID:      m["alipay.app_id"],
		PrivateKey: m["alipay.private_key"],
		PublicKey:  m["alipay.public_key"],
		NotifyURL:  m["alipay.notify_url"],
		Sandbox:    m["alipay.sandbox"] == "true",
	}, nil
}

// client returns a cached alipay client, rebuilding it when the config
// signature changes.
func (p *Provider) client() (*alipay.Client, error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.AppID == "" || cfg.PrivateKey == "" || cfg.PublicKey == "" || cfg.NotifyURL == "" {
		return nil, errors.New("alipay is not configured")
	}
	key := cfg.AppID + "|" + cfg.PrivateKey + "|" + cfg.PublicKey + "|" + strconv.FormatBool(cfg.Sandbox)

	p.mu.RLock()
	if p.cache != nil && p.ckey == key {
		c := p.cache
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache != nil && p.ckey == key {
		return p.cache, nil
	}
	client, err := alipay.New(cfg.AppID, cfg.PrivateKey, !cfg.Sandbox)
	if err != nil {
		return nil, err
	}
	if err := client.LoadAliPayPublicKey(cfg.PublicKey); err != nil {
		return nil, err
	}
	p.cache = client
	p.ckey = key
	return client, nil
}

// PayURL implements payment.Provider. It creates an offline pre-creation order
// (alipay.trade.precreate) and returns the generated QR code string as a "qr"
// PayDirective that the frontend renders for the user to scan.
func (p *Provider) PayURL(order *model.Order, subject string) (*payment.PayDirective, error) {
	client, err := p.client()
	if err != nil {
		return nil, err
	}
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	biz := alipay.TradePreCreate{
		Trade: alipay.Trade{
			Subject:        subject,
			OutTradeNo:     order.OutTradeNo,
			TotalAmount:    yuan(order.Amount),
			NotifyURL:      cfg.NotifyURL,
			TimeoutExpress: "30m",
		},
	}
	rsp, err := client.TradePreCreate(context.Background(), biz)
	if err != nil {
		return nil, err
	}
	return &payment.PayDirective{Kind: "qr", URL: rsp.QRCode}, nil
}

// VerifyNotify implements payment.Provider. Alipay posts an
// application/x-www-form-urlencoded body; we parse it and verify the signature.
func (p *Provider) VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error) {
	if err := r.ParseForm(); err != nil {
		return "", "", false, err
	}
	client, err := p.client()
	if err != nil {
		return "", "", false, err
	}
	if err := client.VerifySign(ctx, r.Form); err != nil {
		return "", "", false, err
	}
	params := r.Form
	outTradeNo = params.Get("out_trade_no")
	tradeNo = params.Get("trade_no")
	tradeStatus := params.Get("trade_status")
	// Only successful payments grant benefits; ignore transient states so they
	// don't flip the order to paid.
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		return outTradeNo, tradeNo, false, nil
	}
	return outTradeNo, tradeNo, true, nil
}

// yuan formats a cents amount as a yuan string with 2 decimals, as required by
// alipay's total_amount field.
func yuan(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64)
}
