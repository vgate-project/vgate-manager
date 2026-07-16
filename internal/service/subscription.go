package service

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	vgcrypto "github.com/vgate-project/vgate-manager/pkg/crypto"

	"github.com/vgate-project/vgate-manager/internal/model"
)

type SubscriptionService struct {
	db *gorm.DB
}

func NewSubscriptionService(db *gorm.DB) *SubscriptionService {
	return &SubscriptionService{db: db}
}

// GetBySubToken looks up a user by their subscription token. Only enabled
// users resolve; disabled/unknown users return ErrRecordNotFound (404).
func (s *SubscriptionService) GetBySubToken(subToken string) (*model.User, error) {
	var user model.User
	// Silence the not-found log row — 404 is the normal "bad token" path.
	res := s.db.Session(&gorm.Session{Logger: s.db.Logger.LogMode(logger.Silent)}).
		Where("sub_token = ? AND enabled = ?", subToken, true).First(&user)
	if res.Error != nil {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

// proxySpec is the client-agnostic, fully-parsed description of one node for a
// given user. Every renderer (v2rayN share-link, Clash YAML) consumes it so the
// node JSON columns are decoded exactly once.
type proxySpec struct {
	Name          string // node.Name → proxy display name
	Address       string // node.Address (host:port) — used verbatim in vless:// URL
	Host          string // host only — used as the Clash `server`
	Port          int    // node.Port — used as the Clash `port`
	Network       string // tcp|ws|xhttp
	Path          string // ws/xhttp path
	Security      string // none|tls|reality
	SNI           string
	AllowInsecure bool
	Flow          string // xtls-rprx-vision or ""
	UUID          string // user.Credential (rotatable VLESS credential)
	RealityPBK    string
	RealitySID    string
	FP            string // client fingerprint (e.g. "chrome")
	V2PBK         string // v2 AEAD public key (when security != reality)
	V2Enabled     bool   // true when vless.Decryption != ""
	XorMode       string // native 0, xorpub 1, random 2
}

// BuildProxySpecs builds a proxySpec for each node assigned to the user. Nodes
// that fail to build (e.g. invalid reality config) are skipped with a warning.
// Virtual child nodes inherit their parent's transport config but use their own
// Address/Name, so they are expanded against the parent before building.
func (s *SubscriptionService) BuildProxySpecs(user *model.User) ([]proxySpec, error) {
	var nodes []model.Node
	if err := s.db.
		Where("nodes.enabled = ?", true).
		Where("nodes.level <= ? OR EXISTS (SELECT 1 FROM user_nodes un WHERE un.node_id = nodes.id AND un.user_id = ? AND un.override = ?)", user.Level, user.ID, true).
		Order("nodes.created_at DESC").
		Find(&nodes).Error; err != nil {
		return nil, err
	}

	// Batch-load parents of any virtual child nodes so we can inherit config.
	parentIDs := make([]string, 0)
	for i := range nodes {
		if nodes[i].ParentID != nil {
			parentIDs = append(parentIDs, *nodes[i].ParentID)
		}
	}
	parents := make(map[string]*model.Node, len(parentIDs))
	if len(parentIDs) > 0 {
		var ps []model.Node
		if err := s.db.Where("id IN ?", parentIDs).Find(&ps).Error; err != nil {
			return nil, err
		}
		for i := range ps {
			parents[ps[i].ID] = &ps[i]
		}
	}

	specs := make([]proxySpec, 0, len(nodes))
	for i := range nodes {
		src := &nodes[i]
		if src.ParentID != nil {
			parent, ok := parents[*src.ParentID]
			if !ok {
				log.Warnf("skip virtual node %s (%s): parent %s not found", src.ID, src.Name, *src.ParentID)
				continue
			}
			// Inherit the parent's transport config; keep the child's identity.
			// A non-zero child Port overrides the inherited parent port (e.g.
			// NAT / port-mapping); 0 means inherit.
			eff := *parent
			eff.ID = src.ID
			eff.Name = src.Name
			eff.Address = src.Address
			if src.Port > 0 {
				eff.Port = src.Port
			}
			src = &eff
		}
		spec, err := buildProxySpec(src, user)
		if err != nil {
			log.Warnf("skip node %s (%s) for user %s: %v", src.ID, src.Name, user.ID, err)
			continue
		}
		specs = append(specs, *spec)
	}
	return specs, nil
}

// BuildLinks builds a vless:// URL for each node assigned to the user. Kept for
// backward compatibility; new code should prefer BuildProxySpecs / Render.
func (s *SubscriptionService) BuildLinks(user *model.User) ([]string, error) {
	specs, err := s.BuildProxySpecs(user)
	if err != nil {
		return nil, err
	}
	links := make([]string, 0, len(specs))
	for i := range specs {
		link, err := specs[i].toVLESSURL()
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, nil
}

// BuildLink constructs a single vless:// share URL from a node + user.
func (s *SubscriptionService) BuildLink(node *model.Node, user *model.User) (string, error) {
	spec, err := buildProxySpec(node, user)
	if err != nil {
		return "", err
	}
	return spec.toVLESSURL()
}

// buildProxySpec decodes a node's JSON config columns into the client-agnostic
// proxySpec, deriving the reality/v2 public keys exactly as the legacy
// BuildLink did.
func buildProxySpec(node *model.Node, user *model.User) (*proxySpec, error) {
	cfg, err := nodeToConfig(node)
	if err != nil {
		return nil, err
	}
	stream := cfg.Stream
	vless := cfg.VLESS

	network := stream.Network
	if network == "" {
		network = "tcp"
	}
	flow := ""
	if node.Flow != nil {
		flow = *node.Flow
	}
	spec := &proxySpec{
		Name:          node.Name,
		Address:       node.Address,
		Port:          node.Port,
		Network:       network,
		Security:      stream.Security,
		Flow:          flow,
		UUID:          user.Credential,
		FP:            "chrome",
		AllowInsecure: node.AllowInsecure,
	}
	if h, _, err := net.SplitHostPort(node.Address); err == nil {
		spec.Host = h
	} else {
		spec.Host = node.Address
	}

	if network == "xhttp" || network == "ws" {
		if path, ok := stream.Settings["path"].(string); ok {
			spec.Path = path
		}
	}

	spec.V2PBK = "none"
	if vless.Decryption != "" {
		v2pbk, err := vgcrypto.DeriveX25519Public(vless.Decryption)
		if err != nil {
			return nil, fmt.Errorf("derive v2 pbk: %w", err)
		}
		spec.V2PBK = v2pbk
		spec.V2Enabled = true
		switch cfg.VLESS.XorMode {
		case 0:
			spec.XorMode = "native"
		case 1:
			spec.XorMode = "xorpub"
		case 2:
			spec.XorMode = "random"
		}

	}

	switch stream.Security {
	case "reality":
		if stream.RealityConfig == nil {
			return nil, errors.New("reality security requires reality_settings")
		}
		rc := stream.RealityConfig
		pbk, err := vgcrypto.DeriveX25519Public(rc.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("derive reality pbk: %w", err)
		}
		spec.RealityPBK = pbk
		if rc.ServerName != "" {
			spec.SNI = rc.ServerName
		}
		if len(rc.ShortIds) > 0 {
			spec.RealitySID = rc.ShortIds[0]
		}
	case "tls":
		if !node.AllowInsecure {
			if stream.TLSConfig != nil && stream.TLSConfig.ServerName != "" {
				spec.SNI = stream.TLSConfig.ServerName
			}
		}
	}

	// Vision flow is mutually exclusive with v2 (enforced at node save).
	if flow == "xtls-rprx-vision" && !spec.V2Enabled {
		spec.Flow = flow
	} else {
		spec.Flow = ""
	}

	return spec, nil
}

// toVLESSURL renders the proxySpec as a vless:// share URL.
func (p *proxySpec) toVLESSURL() (string, error) {
	q := url.Values{}
	q.Set("type", p.Network)
	if p.Network == "xhttp" {
		q.Set("mode", "auto")
	}
	if p.Network == "xhttp" || p.Network == "ws" {
		if p.Path != "" {
			q.Set("path", p.Path)
		}
	}
	q.Set("security", p.Security)

	switch p.Security {
	case "reality":
		q.Set("pbk", p.RealityPBK)
		q.Set("fp", p.FP)
		if p.SNI != "" {
			q.Set("sni", p.SNI)
		}
		if p.RealitySID != "" {
			q.Set("sid", p.RealitySID)
		} else {
			q.Set("sid", "")
		}
	case "tls":
		if p.AllowInsecure {
			q.Set("allowInsecure", "1")
		} else if p.SNI != "" {
			q.Set("sni", p.SNI)
		}
	}

	if p.V2Enabled {
		q.Set("encryption", fmt.Sprintf("mlkem768x25519plus.%s.0rtt.%s", p.XorMode, p.V2PBK))
	}

	if p.Flow != "" {
		q.Set("flow", p.Flow)
	}

	// Address is stored as host:port (see model.Node). When it already
	// carries a port we use it verbatim; otherwise we append the canonical
	// Port so the link is never malformed (e.g. "host:443:443").
	host := p.Address
	if _, _, err := net.SplitHostPort(p.Address); err != nil {
		host = net.JoinHostPort(p.Address, strconv.Itoa(p.Port))
	}
	u := &url.URL{
		Scheme:   "vless",
		User:     url.User(p.UUID),
		Host:     host,
		RawQuery: q.Encode(),
		Fragment: p.Name,
	}
	return u.String(), nil
}

// SubscribeURL builds the user-facing subscription link from a random base
// URL. baseURLs is the admin-configured list of subscription base URLs (bare
// origins, no path); when non-empty one is chosen at random, otherwise the
// provided fallback (typically the request origin) is used. The returned URL
// points at the public subscription endpoint and base64URL appends the
// "?type=v2rayn" query used by QR codes / v2ray-compatible clients.
func (s *SubscriptionService) SubscribeURL(subToken string, baseURLs []string, fallback string) (subURL, base64URL string) {
	base := fallback
	if len(baseURLs) > 0 {
		base = baseURLs[rand.Intn(len(baseURLs))]
	}
	base = strings.TrimRight(base, "/")
	subURL = base + "/api/v1/sub/" + subToken
	return subURL, subURL + "?type=v2rayn"
}

// Render produces the subscription payload for a specific client type.
// clientType is one of: "v2rayn" (base64 share-link list), "clash"
// (Clash.Meta YAML), "raw" (plaintext vless:// list). Anything else is treated
// as "v2rayn". Surge is intentionally unsupported (Surge has no VLESS support);
// callers that pass "surge" get the v2rayn fallback.
func (s *SubscriptionService) Render(user *model.User, clientType string) (contentType string, body []byte, err error) {
	switch clientType {
	case "clash":
		specs, err := s.BuildProxySpecs(user)
		if err != nil {
			return "", nil, err
		}
		out, err := buildClashYAML(specs)
		if err != nil {
			return "", nil, err
		}
		return "application/yaml; charset=utf-8", out, nil
	case "raw":
		links, err := s.BuildLinks(user)
		if err != nil {
			return "", nil, err
		}
		return "text/plain; charset=utf-8", []byte(strings.Join(links, "\n")), nil
	default: // "v2rayn" and any unsupported/unknown type
		links, err := s.BuildLinks(user)
		if err != nil {
			return "", nil, err
		}
		out := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
		return "text/plain; charset=utf-8", []byte(out), nil
	}
}

// ---- Clash.Meta / mihomo YAML ----

type clashRealityOpts struct {
	PublicKey string `yaml:"public-key"`
	ShortId   string `yaml:"short-id"`
}

type clashWSOpts struct {
	Path    string            `yaml:"path,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

type clashXHTTPOpts struct {
	Path string `yaml:"path,omitempty"`
	Mode string `yaml:"mode,omitempty"`
}

type clashProxy struct {
	Name              string            `yaml:"name"`
	Type              string            `yaml:"type"`
	Server            string            `yaml:"server"`
	Port              int               `yaml:"port"`
	UUID              string            `yaml:"uuid"`
	Flow              string            `yaml:"flow,omitempty"`
	Network           string            `yaml:"network,omitempty"`
	TLS               bool              `yaml:"tls,omitempty"`
	ServerName        string            `yaml:"servername,omitempty"`
	ClientFingerprint string            `yaml:"client-fingerprint,omitempty"`
	Encryption        string            `yaml:"encryption,omitempty"`
	RealityOpts       *clashRealityOpts `yaml:"reality-opts,omitempty"`
	WSOpts            *clashWSOpts      `yaml:"ws-opts,omitempty"`
	XHTTPOpts         *clashXHTTPOpts   `yaml:"xhttp-opts,omitempty"`
}

type clashDNS struct {
	Enabled      bool     `yaml:"enabled"`
	EnhancedMode string   `yaml:"enhanced-mode"`
	Nameserver   []string `yaml:"nameserver"`
}

type clashProxyGroup struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Proxies  []string `yaml:"proxies"`
	URL      string   `yaml:"url,omitempty"`
	Interval int      `yaml:"interval,omitempty"`
}

type clashConfig struct {
	MixedPort          int               `yaml:"mixed-port"`
	Mode               string            `yaml:"mode"`
	LogLevel           string            `yaml:"log-level"`
	ExternalController string            `yaml:"external-controller"`
	DNS                clashDNS          `yaml:"dns"`
	Proxies            []clashProxy      `yaml:"proxies"`
	ProxyGroups        []clashProxyGroup `yaml:"proxy-groups"`
	Rules              []string          `yaml:"rules"`
}

// buildClashYAML renders a Clash.Meta / mihomo configuration (VLESS + Reality)
// from the proxy specs.
func buildClashYAML(specs []proxySpec) ([]byte, error) {
	names := make([]string, 0, len(specs))
	proxies := make([]clashProxy, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
		p := clashProxy{
			Name:   s.Name,
			Type:   "vless",
			Server: s.Host,
			Port:   s.Port,
			UUID:   s.UUID,
			Flow:   s.Flow,
		}
		if s.Network != "tcp" {
			p.Network = s.Network
		}
		if s.Security == "tls" || s.Security == "reality" {
			p.TLS = true
		}
		if s.SNI != "" {
			p.ServerName = s.SNI
		}
		if s.Security == "reality" {
			p.ClientFingerprint = s.FP
			p.RealityOpts = &clashRealityOpts{PublicKey: s.RealityPBK, ShortId: s.RealitySID}
		}
		switch s.Network {
		case "ws":
			opts := &clashWSOpts{}
			if s.Path != "" {
				opts.Path = s.Path
			}
			if s.SNI != "" {
				opts.Headers = map[string]string{"Host": s.SNI}
			}
			p.WSOpts = opts
		case "xhttp":
			opts := &clashXHTTPOpts{Mode: "auto"}
			if s.Path != "" {
				opts.Path = s.Path
			}
			p.XHTTPOpts = opts
		}
		if s.V2Enabled {
			p.Encryption = fmt.Sprintf("mlkem768x25519plus.%s.0rtt.%s", s.XorMode, s.V2PBK)
		}
		proxies = append(proxies, p)
	}

	cfg := clashConfig{
		MixedPort:          7890,
		Mode:               "rule",
		LogLevel:           "info",
		ExternalController: "127.0.0.1:9090",
		DNS: clashDNS{
			Enabled:      true,
			EnhancedMode: "fake-ip",
			Nameserver:   []string{"https://1.1.1.1/dns-query"},
		},
		Proxies: proxies,
		ProxyGroups: []clashProxyGroup{
			{
				Name:    "Proxy",
				Type:    "select",
				Proxies: append([]string{"auto"}, append(append([]string{}, names...), "DIRECT")...),
			},
			{
				Name:     "auto",
				Type:     "url-test",
				URL:      "https://www.gstatic.com/generate_204",
				Interval: 300,
				Proxies:  names,
			},
		},
		Rules: []string{"MATCH,Proxy"},
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return append([]byte("# vgate Subscription\n"), out...), nil
}
