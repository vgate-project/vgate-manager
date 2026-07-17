package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	tb "gopkg.in/telebot.v4"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

// TelegramService owns the VGate Telegram bot: its lifecycle, the admin
// alerts, the user self-service commands, and the admin remote-control
// commands. Configuration is sourced from SystemConfig (the telegram.* keys)
// so an admin can enable / reconfigure the bot at runtime without a restart.
//
// The bot process is started by NewRouter via Run(). A background watcher
// reconciles the running bot with the current config (enabled flag, token,
// admin chat IDs) every few seconds and (re)starts or stops it on change.
type TelegramService struct {
	db       *gorm.DB
	sysCfg   *SystemConfigService
	authSvc  *AuthService
	userSvc  *UserService
	annSvc   *AnnouncementService
	orderSvc *OrderService
	nodeSvc  *NodeService
	statsSvc *StatsService
	subSvc   *SubscriptionService

	// botMu guards the live bot instance + its config signature.
	botMu sync.Mutex
	bot   *tb.Bot
	// sig is a stable signature of the bot-affecting config; when it changes
	// the watcher (re)starts the bot.
	sig string

	// prevOnline tracks each node's last observed online state so the monitor
	// can emit node_up / node_down transitions exactly once.
	prevOnline map[string]bool
	// trafficAlerted guards the "traffic exceeded" alert so it fires once
	// per user; cleared automatically when the user drops back under quota
	// (e.g. after a reset).
	trafficAlerted map[string]bool

	// ticketSvc is injected so an admin can reply to a ticket from Telegram by
	// quoting the bot's ticket notification message.
	ticketSvc *TicketService

	// ticketReplyMu guards ticketReplyMap, which maps a Telegram message id
	// (sent by the bot to an admin) back to the ticket it describes, so an
	// admin's quoted reply can be attributed to the right ticket.
	ticketReplyMu  sync.Mutex
	ticketReplyMap map[string]string
}

// NewTelegramService constructs the service. The individual sub-services are
// injected (all in the same package) so the bot can serve user commands and
// admin overviews without re-importing them. nodeSvc and statsSvc may be
// nil at construction and supplied later via SetNodeService /
// SetStatsService (they are created after this call site in the router).
func NewTelegramService(
	db *gorm.DB,
	sysCfg *SystemConfigService,
	authSvc *AuthService,
	userSvc *UserService,
	annSvc *AnnouncementService,
	orderSvc *OrderService,
	nodeSvc *NodeService,
	statsSvc *StatsService,
	subSvc *SubscriptionService,
) *TelegramService {
	return &TelegramService{
		db:             db,
		sysCfg:         sysCfg,
		authSvc:        authSvc,
		userSvc:        userSvc,
		annSvc:         annSvc,
		orderSvc:       orderSvc,
		nodeSvc:        nodeSvc,
		statsSvc:       statsSvc,
		subSvc:         subSvc,
		prevOnline:     map[string]bool{},
		trafficAlerted: map[string]bool{},
		ticketReplyMap: map[string]string{},
	}
}

// SetNodeService injects the node service (used by the /anodes command).
func (s *TelegramService) SetNodeService(svc *NodeService) {
	s.nodeSvc = svc
}

// SetStatsService injects the stats service (used by the /astats command).
func (s *TelegramService) SetStatsService(svc *StatsService) {
	s.statsSvc = svc
}

// SetTicketService injects the ticket service so an admin can reply to a ticket
// from Telegram by quoting the bot's ticket notification message.
func (s *TelegramService) SetTicketService(svc *TicketService) {
	s.ticketSvc = svc
}

// Run starts the bot lifecycle: it reconciles once immediately, then launches
// background loops for config watching, node liveness, and traffic-threshold
// monitoring. Run is non-blocking; it returns after spawning the goroutines.
func (s *TelegramService) Run() {
	s.reconcile()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			s.reconcile()
		}
	}()
	go func() {
		t := time.NewTicker(1 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.monitorNodes()
		}
	}()
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.monitorTraffic()
		}
	}()
}

// reconcile compares the current config to the running bot and starts/stops the
// bot as needed. It is safe to call repeatedly; it is a no-op when nothing
// changed.
func (s *TelegramService) reconcile() {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil {
		return
	}
	sig := fmt.Sprintf("%v|%s", cfg.Enabled, cfg.BotToken)
	s.botMu.Lock()
	defer s.botMu.Unlock()
	if sig == s.sig {
		return
	}
	// Config changed: tear down the old bot (if any) before (re)starting.
	if s.bot != nil {
		s.bot.Stop()
		s.bot = nil
	}
	s.sig = sig
	if !cfg.Enabled || cfg.BotToken == "" {
		log.Infof("telegram: bot stopped (enabled=%v, token_set=%v)", cfg.Enabled, cfg.BotToken != "")
		return
	}
	b, err := tb.NewBot(tb.Settings{
		Token:  cfg.BotToken,
		Poller: &tb.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Errorf("telegram: failed to init bot: %v", err)
		return
	}
	s.registerHandlers(b)
	s.bot = b
	go b.Start()
	log.Infof("telegram: bot started (user_bot=%v)", cfg.UserBotEnabled)
}

// liveBot returns the currently running bot (or nil). Callers must not hold
// botMu while calling bot methods that block.
func (s *TelegramService) liveBot() *tb.Bot {
	s.botMu.Lock()
	defer s.botMu.Unlock()
	return s.bot
}

// isAdmin reports whether the given Telegram chat ID belongs to a linked
// admin (model.Admin.telegram_id). Admins authorize themselves by linking
// their chat via /astart <code> from the console, so the admin identity is the
// linked admin set — there is no separate hand-maintained chat-ID list.
func (s *TelegramService) isAdmin(chatID int64) bool {
	if chatID == 0 || s.authSvc == nil {
		return false
	}
	_, err := s.authSvc.GetAdminByTelegramID(chatID)
	return err == nil
}

// linkedAdminChatIDs returns the Telegram chat IDs of every admin that has
// linked their chat (model.Admin.telegram_id != 0). These are the recipients
// of admin alerts and the chats permitted to issue remote-control commands.
func (s *TelegramService) linkedAdminChatIDs() []int64 {
	var ids []int64
	if err := s.db.Model(&model.Admin{}).
		Where("telegram_id <> ?", 0).
		Pluck("telegram_id", &ids).Error; err != nil {
		log.Errorf("telegram: failed to load linked admins: %v", err)
		return nil
	}
	return ids
}

// SendToAdmin delivers text to every linked admin chat. It is a no-op
// when Telegram is disabled or the bot is not running.
func (s *TelegramService) SendToAdmin(text string) {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil || !cfg.Enabled {
		return
	}
	bot := s.liveBot()
	if bot == nil {
		return
	}
	for _, id := range s.linkedAdminChatIDs() {
		if _, err := bot.Send(&tb.Chat{ID: id}, text); err != nil {
			log.Warnf("telegram: failed to send admin message to %d: %v", id, err)
		}
	}
}

// NotifyUser delivers text to a single user's linked Telegram chat. It is a
// no-op when Telegram is disabled, the bot is not running, or the user has not
// linked a chat / opted out of notifications.
func (s *TelegramService) NotifyUser(userID, text string) {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil || !cfg.Enabled || !cfg.UserBotEnabled {
		return
	}
	var u model.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		return
	}
	if u.TelegramID == 0 || !u.TelegramNotify {
		return
	}
	bot := s.liveBot()
	if bot == nil {
		return
	}
	if _, err := bot.Send(&tb.Chat{ID: u.TelegramID}, text); err != nil {
		log.Warnf("telegram: failed to notify user %s: %v", userID, err)
	}
}

// NotifyAdminsTicket sends a ticket summary to every admin chat so they can
// follow (and reply to) tickets from Telegram. It is gated by the
// alert_ticket toggle and is a no-op when disabled or the bot is not running.
// Each sent message is remembered (chatID:msgID -> ticketID) so an admin can
// reply by quoting it. A "ref:<ticketID>" footer provides a restart-safe
// fallback if the in-memory map is lost.
func (s *TelegramService) NotifyAdminsTicket(t *model.Ticket, content string) {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil {
		log.Warnf("telegram: ticket alert skipped: failed to read config: %v", err)
		return
	}
	if !cfg.Enabled {
		log.Debugf("telegram: ticket alert skipped: telegram.enabled is false")
		return
	}
	if !cfg.AlertTicket {
		log.Debugf("telegram: ticket alert skipped: telegram.alert_ticket is false")
		return
	}
	bot := s.liveBot()
	if bot == nil {
		log.Warnf("telegram: ticket alert skipped: bot is not running (check telegram.enabled and telegram.bot_token)")
		return
	}
	admins := s.linkedAdminChatIDs()
	if len(admins) == 0 {
		log.Warnf("telegram: ticket alert skipped: no admin has linked a Telegram chat (telegram_id = 0)")
		return
	}
	var u model.User
	userLabel := t.UserID
	if err := s.db.First(&u, "id = ?", t.UserID).Error; err == nil && u.Email != "" {
		userLabel = u.Email
	}
	text := fmt.Sprintf("🎫 Ticket: %s\nPriority: %s\nFrom: %s\nStatus: %s\n\n%s\n\nref:%s",
		t.Subject, t.Priority, userLabel, t.Status, content, t.ID)
	for _, id := range admins {
		sent, err := bot.Send(&tb.Chat{ID: id}, text)
		if err != nil {
			log.Warnf("telegram: failed to send ticket alert to admin %d: %v", id, err)
			continue
		}
		if sent != nil {
			s.ticketReplyMu.Lock()
			s.ticketReplyMap[fmt.Sprintf("%d:%d", id, sent.ID)] = t.ID
			s.ticketReplyMu.Unlock()
		}
	}
}

// ticketIDFromReply resolves the ticket a quoted message refers to: first via
// the in-memory map, then via the "ref:<ticketID>" footer embedded in the text.
func (s *TelegramService) ticketIDFromReply(m *tb.Message) string {
	if m == nil {
		return ""
	}
	s.ticketReplyMu.Lock()
	if id, ok := s.ticketReplyMap[fmt.Sprintf("%d:%d", m.Chat.ID, m.ID)]; ok {
		s.ticketReplyMu.Unlock()
		return id
	}
	s.ticketReplyMu.Unlock()
	// Fallback: parse "ref:<ticketID>" from the quoted message text.
	const prefix = "ref:"
	idx := strings.LastIndex(m.Text, prefix)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(m.Text[idx+len(prefix):])
}

// handleAdminTicketReply lets an admin reply to a ticket by quoting the bot's
// ticket notification message. It only acts on replies from admin chats to a
// tracked ticket message; everything else is left untouched so commands and
// ordinary chatter keep working.
func (s *TelegramService) handleAdminTicketReply(c tb.Context) error {
	msg := c.Message()
	if msg.ReplyTo == nil {
		return nil
	}
	if !s.isAdmin(c.Sender().ID) {
		return nil
	}
	ticketID := s.ticketIDFromReply(msg.ReplyTo)
	if ticketID == "" {
		return nil
	}
	content := strings.TrimSpace(c.Text())
	if content == "" {
		return c.Send("Empty reply ignored.")
	}
	if s.ticketSvc == nil {
		return c.Send("Ticket service unavailable.")
	}
	// Map the Telegram chat back to a VGate admin ID so the reply is
	// attributed to the right admin (not the raw Telegram chat ID).
	adminID, err := s.adminIDForChat(c.Sender().ID)
	if err != nil {
		return c.Send("Link your Telegram to an admin account before replying (set admin telegram_id).")
	}
	if _, err := s.ticketSvc.AddAdminMessage(adminID, ticketID, content); err != nil {
		return c.Send("Failed to save reply: " + err.Error())
	}
	return c.Send("✅ Replied to ticket.")
}

// adminIDForChat resolves a Telegram chat ID to the owning VGate admin's
// numeric ID. Admins must link their Telegram chat (admin.telegram_id) for
// reply attribution; an unlinked chat yields an error so replies are never
// mis-attributed to the wrong admin.
func (s *TelegramService) adminIDForChat(chatID int64) (string, error) {
	if s.authSvc == nil {
		return "", errors.New("auth service unavailable")
	}
	admin, err := s.authSvc.GetAdminByTelegramID(chatID)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(admin.ID), 10), nil
}

// NotifyAdminEvent sends text to admins for the named event, but only if that
// event's toggle (telegram.alert_*) is enabled. Unknown event keys are dropped.
func (s *TelegramService) NotifyAdminEvent(eventKey, text string) {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil || !cfg.Enabled {
		return
	}
	var on bool
	switch eventKey {
	case CfgKeyAlertNewRegistration:
		on = cfg.AlertNewReg
	case CfgKeyAlertOrderPaid:
		on = cfg.AlertOrderPaid
	case CfgKeyAlertNodeUp:
		on = cfg.AlertNodeUp
	case CfgKeyAlertNodeDown:
		on = cfg.AlertNodeDown
	case CfgKeyAlertTrafficExceeded:
		on = cfg.AlertTraffic
	case CfgKeyAlertAnnouncement:
		on = cfg.AlertAnnouncement
	default:
		return
	}
	if !on {
		return
	}
	s.SendToAdmin(text)
}

// BroadcastToUsers delivers text to every linked user who opted into Telegram
// notifications. It is a no-op when disabled or the bot is not running. Returns
// the number of recipients successfully reached and the total targeted.
func (s *TelegramService) BroadcastToUsers(text string) (sent, total int) {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil || !cfg.Enabled || !cfg.UserBotEnabled {
		return 0, 0
	}
	bot := s.liveBot()
	if bot == nil {
		return 0, 0
	}
	var users []model.User
	if err := s.db.Where("telegram_id <> 0 AND telegram_notify = ?", true).Find(&users).Error; err != nil {
		log.Errorf("telegram: broadcast query failed: %v", err)
		return 0, 0
	}
	for _, u := range users {
		if _, err := bot.Send(&tb.Chat{ID: u.TelegramID}, text); err != nil {
			log.Warnf("telegram: failed to broadcast to user %s: %v", u.Email, err)
		} else {
			sent++
		}
	}
	return sent, len(users)
}

// monitorNodes compares each node's current online state to the last observed
// state and emits a single node_up / node_down alert on transition.
func (s *TelegramService) monitorNodes() {
	var nodes []model.Node
	if err := s.db.Find(&nodes).Error; err != nil {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		online := n.IsOnline()
		prev, seen := s.prevOnline[n.ID]
		if !seen {
			s.prevOnline[n.ID] = online
			continue
		}
		if online == prev {
			continue
		}
		s.prevOnline[n.ID] = online
		if online {
			s.NotifyAdminEvent(CfgKeyAlertNodeUp, fmt.Sprintf("Node %q is back online.", n.Name))
		} else {
			s.NotifyAdminEvent(CfgKeyAlertNodeDown, fmt.Sprintf("Node %q went offline.", n.Name))
		}
	}
}

// monitorTraffic scans users with a finite quota and alerts once when their
// cumulative usage reaches the quota. The guard is cleared automatically when a
// user drops back under quota (e.g. after a monthly / plan reset).
func (s *TelegramService) monitorTraffic() {
	var users []model.User
	if err := s.db.Where("quota_bytes > 0").Find(&users).Error; err != nil {
		return
	}
	for i := range users {
		u := &users[i]
		over := (u.UpTotal + u.DownTotal) >= u.QuotaBytes
		if over {
			if !s.trafficAlerted[u.ID] {
				s.trafficAlerted[u.ID] = true
				s.NotifyAdminEvent(CfgKeyAlertTrafficExceeded,
					fmt.Sprintf("User %s has exceeded the traffic quota (%s / %s).",
						u.Email, formatBytes(u.UpTotal+u.DownTotal), formatBytes(u.QuotaBytes)))
			}
		} else {
			delete(s.trafficAlerted, u.ID)
		}
	}
}

// registerHandlers wires the inbound command handlers onto a telebot instance.
func (s *TelegramService) registerHandlers(b *tb.Bot) {
	// Public entry point. With a payload it binds the chat to a VGate account;
	// without it, it shows the help text.
	b.Handle("/start", func(c tb.Context) error {
		args := c.Args()
		if len(args) == 0 {
			return s.handleHelp(c)
		}
		// Telegram deep links can only carry a payload via /start, so the
		// bind code is prefixed (a_ = admin, u_ = user) to route it.
		switch {
		case strings.HasPrefix(args[0], telegramBindPrefixAdmin):
			return s.handleAdminBind(c, args[0])
		default:
			return s.handleBind(c, args[0])
		}
	})
	b.Handle("/status", s.handleStatus)
	b.Handle("/sub", s.handleSub)
	b.Handle("/unbind", s.handleUnbind)
	b.Handle("/help", s.handleHelp)

	// Master command for admins to link their own Telegram chat so they
	// can reply to tickets from the bot. The code is issued from the admin
	// console (POST /admin/me/telegram/bind).
	b.Handle("/astart", func(c tb.Context) error {
		args := c.Args()
		if len(args) > 0 {
			return s.handleAdminBind(c, args[0])
		}
		return s.handleAdminHelp(c)
	})

	// Admin-only remote-control commands.
	b.Handle("/ahelp", s.handleAdminHelp)
	b.Handle("/astats", s.handleAdminStats)
	b.Handle("/anodes", s.handleAdminNodes)
	b.Handle("/ausers", s.handleAdminUsers)
	b.Handle("/abroadcast", s.handleAdminBroadcast)
	b.Handle("/aannounce", s.handleAdminAnnounce)

	// Any plain-text reply (not a command) that quotes a ticket notification
	// message is treated as an admin reply to that ticket. Non-matching text
	// is ignored (returns nil) so commands and ordinary chat still work.
	b.Handle(tb.OnText, s.handleAdminTicketReply)
}

// handleBind links the sending Telegram chat to the VGate account that issued
// the one-time bind code. The code is redeemed from the user portal.
func (s *TelegramService) handleBind(c tb.Context, code string) error {
	now := time.Now()
	var u model.User
	if err := s.db.Where("telegram_bind_token = ? AND telegram_bind_expires_at > ?", stripBindPrefix(code), now).First(&u).Error; err != nil {
		return c.Send("Invalid or expired bind code. Please generate a new one from the user portal.")
	}
	wasBound := u.TelegramID != 0
	u.TelegramID = c.Sender().ID
	u.TelegramBoundAt = &now
	u.TelegramNotify = true
	u.TelegramBindToken = ""
	u.TelegramBindExpiresAt = nil
	if err := s.db.Save(&u).Error; err != nil {
		return c.Send("Failed to link your account. Please try again.")
	}
	delete(s.trafficAlerted, u.ID)
	if err := c.Send(fmt.Sprintf("Your Telegram account is now linked to %s.", u.Email)); err != nil {
		return err
	}
	// On a first-ever bind, surface the available commands so the user
	// discovers what the bot can do.
	if !wasBound {
		return c.Send(s.userCommandsText())
	}
	return nil
}

// userCommandsText returns the list of bot commands available to a linked
// user. It is sent on first bind so new users discover what they can do.
func (s *TelegramService) userCommandsText() string {
	return "Here are the commands you can use:\n" +
		"/status - show your traffic, quota and expiry\n" +
		"/sub - get your subscription link\n" +
		"/unbind - unlink this Telegram account\n" +
		"/help - show help message"
}

// handleHelp shows the public command list to unbound or bound users.
func (s *TelegramService) handleHelp(c tb.Context) error {
	help := "VGate Telegram Bot\n\n" +
		"Link your account from the user portal, then use:\n" +
		"/status - show your traffic, quota and expiry\n" +
		"/sub - get your subscription link\n" +
		"/unbind - unlink this Telegram account\n" +
		"/help - show this message"
	return c.Send(help)
}

// handleStatus reports the caller's own usage summary.
func (s *TelegramService) handleStatus(c tb.Context) error {
	var u model.User
	if err := s.db.Where("telegram_id = ?", c.Sender().ID).First(&u).Error; err != nil {
		return c.Send("Your Telegram account is not linked to any VGate account. Use /start <code> from the user portal.")
	}
	quota := "Unlimited"
	if u.QuotaBytes > 0 {
		used := u.UpTotal + u.DownTotal
		pct := float64(used) / float64(u.QuotaBytes) * 100
		quota = fmt.Sprintf("%s / %s (%.0f%%)", formatBytes(used), formatBytes(u.QuotaBytes), pct)
	}
	expiry := "never"
	if u.ExpireAt != nil {
		expiry = u.ExpireAt.Format("2006-01-02 15:04")
	}
	enabled := "yes"
	if !u.Enabled {
		enabled = "no"
	}
	text := fmt.Sprintf("Account: %s\nEnabled: %s\nTraffic: %s\nExpires: %s",
		u.Email, enabled, quota, expiry)
	return c.Send(text)
}

// handleSub returns the caller's subscription link.
func (s *TelegramService) handleSub(c tb.Context) error {
	var u model.User
	if err := s.db.Where("telegram_id = ?", c.Sender().ID).First(&u).Error; err != nil {
		return c.Send("Your Telegram account is not linked to any VGate account. Use /start <code> from the user portal.")
	}
	baseURLs, _ := s.sysCfg.GetSubBaseURLs()
	subURL, base64URL := s.subSvc.SubscribeURL(u.SubToken, baseURLs, "")
	text := fmt.Sprintf("Subscription link:\n%s\n\nv2ray base64:\n%s", subURL, base64URL)
	return c.Send(text)
}

// handleUnbind clears the Telegram link for the caller's account.
func (s *TelegramService) handleUnbind(c tb.Context) error {
	res := s.db.Model(&model.User{}).
		Where("telegram_id = ?", c.Sender().ID).
		Updates(map[string]any{
			"telegram_id":              0,
			"telegram_bound_at":        nil,
			"telegram_notify":          false,
			"telegram_bind_token":      "",
			"telegram_bind_expires_at": nil,
		})
	if res.Error != nil {
		return c.Send("Failed to unlink. Please try again.")
	}
	if res.RowsAffected == 0 {
		return c.Send("This Telegram account is not linked to any VGate account.")
	}
	return c.Send("Your Telegram account has been unlinked.")
}

// handleAdminHelp lists the admin remote-control commands.
func (s *TelegramService) handleAdminHelp(c tb.Context) error {
	if !s.isAdmin(c.Sender().ID) {
		return c.Send("Unauthorized.")
	}
	help := "VGate Admin Commands\n\n" +
		"/astats - dashboard overview\n" +
		"/anodes - node list with status\n" +
		"/ausers - recent users\n" +
		"/abroadcast <text> - message all linked users\n" +
		"/aannounce <title> | <body> - create + broadcast an announcement"
	return c.Send(help)
}

// handleAdminStats replies with the dashboard overview.
func (s *TelegramService) handleAdminStats(c tb.Context) error {
	if !s.isAdmin(c.Sender().ID) {
		return c.Send("Unauthorized.")
	}
	if s.statsSvc == nil {
		return c.Send("Statistics service is not ready.")
	}
	ov, err := s.statsSvc.GetOverview()
	if err != nil {
		return c.Send("Failed to load statistics.")
	}
	text := fmt.Sprintf("Overview\nUsers: %d (active 24h: %d)\nNodes: %d (online: %d)\nTraffic 24h: %s up / %s down",
		ov.UserCount, ov.OnlineUsers24h, ov.NodeCount, ov.NodeOnline,
		formatBytes(ov.Up24h), formatBytes(ov.Down24h))
	return c.Send(text)
}

// handleAdminNodes replies with the node list and online state.
func (s *TelegramService) handleAdminNodes(c tb.Context) error {
	if !s.isAdmin(c.Sender().ID) {
		return c.Send("Unauthorized.")
	}
	if s.nodeSvc == nil {
		return c.Send("Node service is not ready.")
	}
	nodes, _, err := s.nodeSvc.List(1, 100, "")
	if err != nil {
		return c.Send("Failed to load nodes.")
	}
	if len(nodes) == 0 {
		return c.Send("No nodes configured.")
	}
	var b strings.Builder
	b.WriteString("Nodes:\n")
	for _, n := range nodes {
		state := "offline"
		if n.Online {
			state = "online"
		}
		fmt.Fprintf(&b, "- %s [%s]\n", n.Name, state)
	}
	return c.Send(b.String())
}

// handleAdminUsers replies with the most recent users.
func (s *TelegramService) handleAdminUsers(c tb.Context) error {
	if !s.isAdmin(c.Sender().ID) {
		return c.Send("Unauthorized.")
	}
	users, _, err := s.userSvc.List(UserListFilter{}, 1, 10)
	if err != nil {
		return c.Send("Failed to load users.")
	}
	if len(users) == 0 {
		return c.Send("No users yet.")
	}
	var b strings.Builder
	b.WriteString("Recent users:\n")
	for _, u := range users {
		expiry := "never"
		if u.ExpireAt != nil {
			expiry = u.ExpireAt.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "- %s (expires %s)\n", u.Email, expiry)
	}
	return c.Send(b.String())
}

// handleAdminBroadcast sends a message to every linked, opted-in user.
func (s *TelegramService) handleAdminBroadcast(c tb.Context) error {
	if !s.isAdmin(c.Sender().ID) {
		return c.Send("Unauthorized.")
	}
	text := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/abroadcast"))
	if text == "" {
		return c.Send("Usage: /abroadcast <text>")
	}
	s.BroadcastToUsers(text)
	return c.Send("Broadcast queued.")
}

// handleAdminAnnounce creates an announcement and broadcasts it to linked users
// (when the announcement alert toggle is enabled).
func (s *TelegramService) handleAdminAnnounce(c tb.Context) error {
	if !s.isAdmin(c.Sender().ID) {
		return c.Send("Unauthorized.")
	}
	raw := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/aannounce"))
	parts := strings.SplitN(raw, "|", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return c.Send("Usage: /aannounce <title> | <body>")
	}
	title := strings.TrimSpace(parts[0])
	body := strings.TrimSpace(parts[1])
	a, err := s.annSvc.Create(title, body, false, true, 0)
	if err != nil {
		return c.Send("Failed to create announcement.")
	}
	// Broadcast if the admin enabled announcement alerts; Create() already
	// broadcasts when enabled, so only send here if it did not.
	if !s.sysCfgAlertAnnouncement() {
		s.BroadcastToUsers(fmt.Sprintf("%s\n\n%s", title, body))
	}
	return c.Send(fmt.Sprintf("Announcement %s created.", a.ID))
}

// sysCfgAlertAnnouncement reports whether the announcement alert is enabled.
func (s *TelegramService) sysCfgAlertAnnouncement() bool {
	cfg, err := s.sysCfg.GetTelegramConfig()
	if err != nil {
		return false
	}
	return cfg.AlertAnnouncement
}

// telegram bind-code payload prefixes. Telegram deep links can only carry a
// payload via /start, so we prefix the one-time code to route the redeeming
// chat to the right account type (user vs admin). The prefixed form is what we
// persist as the bind token and what the deep link embeds.
const (
	telegramBindPrefixUser  = "u_"
	telegramBindPrefixAdmin = "a_"
)

// stripBindPrefix removes a leading u_/a_ prefix if present, so codes typed or
// pasted by hand (e.g. "/astart a_xxx") still redeem correctly.
func stripBindPrefix(code string) string {
	return strings.TrimPrefix(strings.TrimPrefix(code, telegramBindPrefixAdmin), telegramBindPrefixUser)
}

// BindCode issues a one-time bind code for the given user and returns the deep
// link the user should open. The code (prefixed with u_) is valid for 10
// minutes. The prefix lets a single /start deep link route to the user flow.
func (s *TelegramService) BindCode(userID string) (code, deepLink, tgLink string, err error) {
	var u model.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		return "", "", "", err
	}
	// The DB token is the bare random string; the u_ prefix is only added
	// for the /start deep link and stripped again at redemption time.
	raw := util.RandomToken(16)
	code = telegramBindPrefixUser + raw
	u.TelegramBindToken = raw
	u.TelegramBindExpiresAt = new(time.Now().Add(10 * time.Minute))
	if err := s.db.Save(&u).Error; err != nil {
		return "", "", "", err
	}
	username := ""
	if cfg, cerr := s.sysCfg.GetTelegramConfig(); cerr == nil {
		username = cfg.BotUsername
	}
	deepLink = "https://t.me/" + username + "?start=" + code
	if username == "" {
		// Without a known bot username fall back to the raw code.
		deepLink = code
	} else {
		// Native-app deep link: opens the Telegram app directly and sends
		// /start <code>. Used by the user portal "Open in Telegram" button so
		// binding is not forced through a browser tab (which only lands on the
		// bot's t.me homepage and never runs /start).
		tgLink = "tg://resolve?domain=" + username + "&start=" + code
	}
	return code, deepLink, tgLink, nil
}

// AdminBindCode issues a one-time bind code for the given admin and returns the
// deep link the admin should open. The code (prefixed with a_) is valid for 10
// minutes and is embedded in /start so the admin deep link routes to the admin
// flow rather than the user flow.
func (s *TelegramService) AdminBindCode(adminID uint) (code, deepLink, tgLink string, err error) {
	var a model.Admin
	if err := s.db.First(&a, "id = ?", adminID).Error; err != nil {
		return "", "", "", err
	}
	// The DB token is the bare random string; the a_ prefix is only added
	// for the /start deep link and stripped again at redemption time.
	raw := util.RandomToken(16)
	code = telegramBindPrefixAdmin + raw
	a.TelegramBindToken = raw
	a.TelegramBindExpiresAt = new(time.Time)
	*a.TelegramBindExpiresAt = time.Now().Add(10 * time.Minute)
	if err := s.db.Save(&a).Error; err != nil {
		return "", "", "", err
	}
	username := ""
	if cfg, cerr := s.sysCfg.GetTelegramConfig(); cerr == nil {
		username = cfg.BotUsername
	}
	deepLink = "https://t.me/" + username + "?start=" + code
	if username == "" {
		deepLink = code
	} else {
		tgLink = "tg://resolve?domain=" + username + "&start=" + code
	}
	return code, deepLink, tgLink, nil
}

// AdminUnbindByID clears the Telegram link for the given admin (admin-initiated).
func (s *TelegramService) AdminUnbindByID(adminID uint) error {
	return s.authSvc.SetAdminTelegram(adminID, 0)
}

// handleAdminBind links the sending Telegram chat to the admin that issued the
// one-time bind code (master command /astart <code>). Only the chat that owns
// the linked admin may then reply to tickets via the bot.
func (s *TelegramService) handleAdminBind(c tb.Context, code string) error {
	now := time.Now()
	var a model.Admin
	if err := s.db.Where("telegram_bind_token = ? AND telegram_bind_expires_at > ?", stripBindPrefix(code), now).First(&a).Error; err != nil {
		return c.Send("Invalid or expired bind code. Generate a new one from the admin console.")
	}
	if err := s.authSvc.SetAdminTelegram(a.ID, c.Sender().ID); err != nil {
		return c.Send("Failed to link your account. Please try again.")
	}
	return c.Send(fmt.Sprintf("Your Telegram account is now linked to admin %q.", a.Username))
}

// UnbindByUser clears the Telegram link for the given user (user-initiated).
func (s *TelegramService) UnbindByUser(userID string) error {
	res := s.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"telegram_id":              0,
			"telegram_bound_at":        nil,
			"telegram_notify":          false,
			"telegram_bind_token":      "",
			"telegram_bind_expires_at": nil,
		})
	if res.Error != nil {
		return res.Error
	}
	delete(s.trafficAlerted, userID)
	return nil
}

// SetNotify toggles the user's opt-in for announcement broadcasts.
func (s *TelegramService) SetNotify(userID string, notify bool) error {
	return s.db.Model(&model.User{}).
		Where("id = ?", userID).
		Update("telegram_notify", notify).Error
}

// TelegramStatus describes a user's current Telegram link state, returned to
// the user portal.
type TelegramStatus struct {
	Bound     bool `json:"bound"`
	Notify    bool `json:"telegram_notify"`
	Available bool `json:"available"`
}

// StatusForUser returns the link state for the given user. Available reports
// whether the user can actually bind right now (bot is live and user-binding
// is enabled by the admin); the user portal hides the Telegram card when false.
func (s *TelegramService) StatusForUser(userID string) (TelegramStatus, error) {
	var u model.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		return TelegramStatus{}, err
	}
	cfg, _ := s.sysCfg.GetTelegramConfig()
	available := s.liveBot() != nil && cfg.Enabled && cfg.UserBotEnabled
	return TelegramStatus{
		Bound:     u.TelegramID != 0,
		Notify:    u.TelegramNotify,
		Available: available,
	}, nil
}

// StatusForAdmin returns the link state for the given admin. Bound reports
// whether the admin has linked a Telegram chat (telegram_id != 0). The admin
// console uses this to render the correct "bound/unbound" state after a
// refresh, since the link is persisted server-side and not in local UI state.
func (s *TelegramService) StatusForAdmin(adminID uint) (TelegramStatus, error) {
	var a model.Admin
	if err := s.db.First(&a, "id = ?", adminID).Error; err != nil {
		return TelegramStatus{}, err
	}
	cfg, _ := s.sysCfg.GetTelegramConfig()
	// Admins can bind whenever the shared bot is live and Telegram is enabled
	// (unlike users, there is no separate user-bot gate for admins).
	available := s.liveBot() != nil && cfg.Enabled
	return TelegramStatus{
		Bound:     a.TelegramID != 0,
		Available: available,
	}, nil
}

// formatBytes renders a byte count in human-readable form (e.g. "1.2 GB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}
