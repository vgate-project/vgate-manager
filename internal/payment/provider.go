// Package payment defines a provider abstraction over payment gateways so the
// order service is not hardcoded to alipay. A gateway (alipay, wechat, stripe,
// ...) implements Provider and is registered in Registry by its platform name.
// The package deliberately imports only model (never service) to avoid an
// import cycle; configuration is supplied via an injected function.
package payment

import (
	"context"
	"net/http"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// PayDirective tells the frontend how to present the payment to the user.
type PayDirective struct {
	// Kind is "redirect" (open URL in a browser, e.g. stripe checkout) or
	// "qr" (render URL as a QR code the user scans, e.g. alipay
	// trade.precreate qr_code / wechat NATIVE code_url).
	Kind string
	// URL is the browser redirect URL when Kind="redirect", or the QR code
	// content (e.g. an alipay qr_code or wechat code_url) when Kind="qr".
	URL string
}

// Provider abstracts a single payment gateway. Adding a new platform means
// implementing Provider and registering it in Registry.Get.
type Provider interface {
	// Platform returns the canonical platform identifier stored on
	// Order.Platform (e.g. model.OrderPlatformAlipay).
	Platform() string

	// Mode reports how the frontend should present the PayDirective returned by
	// PayURL. Known values: "redirect" (open URL in a browser, e.g. stripe /
	// paypal checkout), "qr" (render URL as a QR code to scan, e.g. alipay /
	// wechat NATIVE), and "iap" (an in-app purchase the user completes inside a
	// native app, e.g. Apple App Store — there is no URL to open).
	Mode() string

	// PayURL returns a PayDirective describing how the user pays for order.
	PayURL(order *model.Order, subject string) (*PayDirective, error)

	// VerifyNotify verifies an async gateway notification from the raw request
	// (compatible with alipay form posts, wechat/stripe JSON callbacks and
	// signature headers). It returns the out_trade_no used to look up the
	// order, the gateway's transaction id, and whether the notification
	// represents a successful (paid) payment. Transient non-paid states return
	// paid=false with a nil error so the gateway can be told the notification
	// was received without granting entitlement.
	VerifyNotify(ctx context.Context, r *http.Request) (outTradeNo, tradeNo string, paid bool, err error)
}

// ConfigStatusProvider is optionally implemented by providers that can report
// whether they are fully configured (i.e. the required credentials are
// present in SystemConfig). The Registry uses it to build the list of
// available payment methods surfaced to the frontend; providers that do not
// implement it are always reported as configured.
type ConfigStatusProvider interface {
	// IsConfigured reports whether the provider has the credentials it needs
	// to actually collect a payment.
	IsConfigured() bool
}

// ChannelMode enumerates the values returned by Provider.Mode.
const (
	ModeRedirect = "redirect"
	ModeQR       = "qr"
	ModeIAP      = "iap"
)

// ChannelInfo describes a registered payment platform for the "list available
// methods" endpoint. Enabled reflects the admin's explicit on/off toggle
// (payment.<platform>.enabled; absent = enabled for backward compatibility);
// Configured reflects whether the provider's credentials are present.
type ChannelInfo struct {
	Platform   string `json:"platform"`
	Label      string `json:"label"`
	Mode       string `json:"mode"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
}

// PlatformLabel maps a platform identifier to a human-readable label used by
// the frontend and the available-methods endpoint.
var PlatformLabel = map[string]string{
	model.OrderPlatformAlipay: "Alipay",
	model.OrderPlatformWechat: "WeChat Pay",
	model.OrderPlatformStripe: "Stripe",
	model.OrderPlatformPaypal: "PayPal",
	model.OrderPlatformApple:  "Apple (App Store)",
}

// PlatformEnabledKey returns the SystemConfig key that holds the admin's
// explicit on/off toggle for the given platform.
func PlatformEnabledKey(platform string) string {
	return "payment." + platform + ".enabled"
}

// ConfigSource supplies the raw SystemConfig key/value map. Injected as a
// function (sysCfg.GetAll) so this package never imports service, which would
// create a cycle (service depends on payment).
type ConfigSource func() (map[string]string, error)
