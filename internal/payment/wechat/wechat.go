// Package wechat implements payment.Provider for WeChat Pay v3 NATIVE. The
// PayURL call creates a NATIVE order and returns the code_url as a "qr"
// PayDirective that the frontend renders as a QR code for the user to scan.
// Async notifications are JSON-signed; the platform certificate is fetched
// lazily (every 6h) to verify the signature, then the resource is decrypted
// with the APIv3 key. Credentials are read lazily from SystemConfig
// (wechat.* keys) through the injected ConfigSource.
package wechat

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-pay/crypto/xpem"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/wechat/v3"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
)

// Platform is the canonical identifier stored on Order.Platform.
const Platform = model.OrderPlatformWechat

// certRefresh is how often the cached WeChat platform cert map is refreshed.
const certRefresh = 6 * time.Hour

// payTimeout is how long a NATIVE order stays payable.
const payTimeout = 30 * time.Minute

// Config holds wechat credentials read from SystemConfig (wechat.* keys).
type Config struct {
	AppID      string
	MchID      string
	APIV3Key   string
	SerialNo   string
	PrivateKey string
	NotifyURL  string
}

// Provider is the wechat implementation of payment.Provider.
type Provider struct {
	getConfig payment.ConfigSource
	mu        sync.RWMutex
	client    *wechat.ClientV3
	ckey      string // config signature for client caching
	apiV3Key  string // cached alongside the client for notify decryption
	certs     map[string]*rsa.PublicKey
	certTS    time.Time
}

// NewProvider builds a wechat Provider. The wechat client is created lazily on
// first use and cached.
func NewProvider(getConfig payment.ConfigSource) (payment.Provider, error) {
	return &Provider{getConfig: getConfig}, nil
}

// Register wires the wechat Provider into the given Registry under its platform
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
	return Config{
		AppID:      m["wechat.app_id"],
		MchID:      m["wechat.mch_id"],
		APIV3Key:   m["wechat.api_v3_key"],
		SerialNo:   m["wechat.serial_no"],
		PrivateKey: m["wechat.private_key"],
		NotifyURL:  m["wechat.notify_url"],
	}, nil
}

// client returns a cached wechat client, rebuilding it when the config
// signature changes.
func (p *Provider) getClient() (*wechat.ClientV3, error) {
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.AppID == "" || cfg.MchID == "" || cfg.APIV3Key == "" ||
		cfg.SerialNo == "" || cfg.PrivateKey == "" || cfg.NotifyURL == "" {
		return nil, errors.New("wechat is not configured")
	}
	key := cfg.MchID + "|" + cfg.SerialNo + "|" + cfg.APIV3Key
	p.mu.RLock()
	if p.client != nil && p.ckey == key {
		c := p.client
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil && p.ckey == key {
		return p.client, nil
	}
	// NewClientV3(mchid, serialNo, apiV3Key, privateKey)
	c, err := wechat.NewClientV3(cfg.MchID, cfg.SerialNo, cfg.APIV3Key, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	p.client = c
	p.ckey = key
	p.apiV3Key = cfg.APIV3Key
	p.certs = nil // drop certs cached under the old key
	return c, nil
}

// certMap returns the cached WeChat platform public-key map (serial -> key),
// refreshing it from the platform certificate endpoint when stale.
func (p *Provider) certMap() (map[string]*rsa.PublicKey, error) {
	p.mu.RLock()
	if p.certs != nil && time.Since(p.certTS) < certRefresh {
		m := p.certs
		p.mu.RUnlock()
		return m, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.certs != nil && time.Since(p.certTS) < certRefresh {
		return p.certs, nil
	}
	c, err := p.getClient()
	if err != nil {
		return nil, err
	}
	_, snCertMap, err := c.GetAndSelectNewestCert()
	if err != nil {
		return nil, err
	}
	m := make(map[string]*rsa.PublicKey, len(snCertMap))
	for sn, pem := range snCertMap {
		pk, err := xpem.DecodePublicKey([]byte(pem))
		if err != nil {
			return nil, fmt.Errorf("wechat: decode platform cert %s: %w", sn, err)
		}
		m[sn] = pk
	}
	p.certs = m
	p.certTS = time.Now()
	return m, nil
}

// PayURL implements payment.Provider. It creates a NATIVE order and returns the
// code_url wrapped as a QR PayDirective.
func (p *Provider) PayURL(order *model.Order, subject string) (*payment.PayDirective, error) {
	c, err := p.getClient()
	if err != nil {
		return nil, err
	}
	cfg, err := p.loadConfig()
	if err != nil {
		return nil, err
	}
	bm := make(gopay.BodyMap)
	bm.Set("appid", cfg.AppID).
		Set("description", subject).
		Set("out_trade_no", order.OutTradeNo).
		Set("notify_url", cfg.NotifyURL).
		Set("time_expire", time.Now().Add(payTimeout).Format(time.RFC3339)).
		SetBodyMap("amount", func(bm gopay.BodyMap) {
			bm.Set("total", order.Amount). // amount is already in cents (分)
							Set("currency", "CNY")
		})
	rsp, err := c.V3TransactionNative(context.Background(), bm)
	if err != nil {
		return nil, err
	}
	if rsp.Code != wechat.Success {
		return nil, fmt.Errorf("wechat native pay failed: %s", rsp.Error)
	}
	return &payment.PayDirective{Kind: "qr", URL: rsp.Response.CodeUrl}, nil
}

// VerifyNotify implements payment.Provider. WeChat posts a JSON body signed
// with its platform private key; we verify the signature with the platform
// public cert, then AES-decrypt the resource with the APIv3 key.
func (p *Provider) VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error) {
	notifyReq, err := wechat.V3ParseNotify(r)
	if err != nil {
		return "", "", false, err
	}
	certMap, err := p.certMap()
	if err != nil {
		return "", "", false, err
	}
	if err := notifyReq.VerifySignByPKMap(certMap); err != nil {
		return "", "", false, err
	}
	// apiV3Key is cached on the client; reload config if the client was rebuilt.
	cfg, err := p.loadConfig()
	if err != nil {
		return "", "", false, err
	}
	result, err := notifyReq.DecryptPayCipherText(cfg.APIV3Key)
	if err != nil {
		return "", "", false, err
	}
	// Only a successful transaction grants entitlement; transient states are
	// acknowledged (paid=false) without flipping the order.
	if result.TradeState != wechat.TradeStateSuccess {
		return result.OutTradeNo, result.TransactionId, false, nil
	}
	return result.OutTradeNo, result.TransactionId, true, nil
}
