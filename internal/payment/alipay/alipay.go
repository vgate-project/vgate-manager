// Package alipay implements payment.Provider for Alipay's offline QR pre-creation
// (alipay.trade.precreate, 统一收单线下交易预创建) and async notification
// verification, using github.com/go-pay/gopay/alipay (classic RSA2 public-key
// gateway). PayURL returns a "qr" PayDirective whose URL is the pre-created QR
// code string the user scans to pay. Credentials are read lazily from
// SystemConfig (alipay.* keys) through the injected ConfigSource, and the alipay
// client is cached and rebuilt only when the config signature changes.
package alipay

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"

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
	client, err := alipay.NewClient(cfg.AppID, cfg.PrivateKey, !cfg.Sandbox)
	if err != nil {
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
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", order.OutTradeNo).
		Set("total_amount", yuan(order.Amount)).
		Set("subject", subject).
		Set("notify_url", cfg.NotifyURL).
		Set("timeout_express", "30m")
	rsp, err := client.TradePrecreate(context.Background(), bm)
	if err != nil {
		return nil, err
	}
	if rsp.Response == nil || rsp.Response.Code != "10000" {
		msg := ""
		if rsp.Response != nil {
			msg = rsp.Response.Msg + " " + rsp.Response.SubMsg
		}
		return nil, errors.New("alipay precreate failed: " + msg)
	}
	return &payment.PayDirective{Kind: "qr", URL: rsp.Response.QrCode}, nil
}

// VerifyNotify implements payment.Provider. Alipay posts an
// application/x-www-form-urlencoded body; we parse it, verify the signature
// with the configured alipay public key (public-key mode), then read the
// trade fields.
func (p *Provider) VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error) {
	bm, err := alipay.ParseNotifyToBodyMap(r)
	if err != nil {
		return "", "", false, err
	}
	cfg, err := p.loadConfig()
	if err != nil {
		return "", "", false, err
	}
	if ok, verr := alipay.VerifySign(cfg.PublicKey, bm); verr != nil || !ok {
		return "", "", false, verr
	}
	outTradeNo = bm.GetString("out_trade_no")
	tradeNo = bm.GetString("trade_no")
	tradeStatus := bm.GetString("trade_status")
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
