// Package api wires the gin engine: middleware, route groups, and handler
// registration. Feature handlers register their own routes via Register* funcs.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/config"
	"github.com/vgate-project/vgate-manager/internal/api/handler"
	"github.com/vgate-project/vgate-manager/internal/middleware"
	"github.com/vgate-project/vgate-manager/internal/payment"
	"github.com/vgate-project/vgate-manager/internal/payment/alipay"
	"github.com/vgate-project/vgate-manager/internal/payment/stripe"
	"github.com/vgate-project/vgate-manager/internal/payment/wechat"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// NewRouter builds the gin engine with global middleware and a /health probe.
// Route groups are registered by feature packages via their Register funcs.
// sysCfg is the single shared SystemConfigService instance; it is reused for the
// CORS middleware, billing (alipay) and the admin system-config endpoints so
// that the in-memory cache stays coherent (an admin PUT refreshes every reader).
// srv is the live *http.Server; the system-config handler hot-applies server
// read/write timeouts to it on save.
func NewRouter(db *gorm.DB, cfg *config.Config, authSvc *service.AuthService, sysCfg *service.SystemConfigService, srv *http.Server) *gin.Engine {
	if cfg.Log.Level == "debug" || cfg.Log.Level == "trace" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.Logger())

	// CORS reads from the shared system_configs cache, so admin edits to
	// cors.allowed_origins take effect without a restart.
	r.Use(middleware.CORS(sysCfg))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Server-facing API (node auth: node_id + token query params).
	serverSvc := service.NewServerService(db)
	serverHandler := handler.NewServerHandler(serverSvc)
	serverGroup := r.Group("/api/v1/server")
	serverGroup.Use(middleware.NodeAuth(db))
	{
		serverGroup.GET("/config", serverHandler.GetConfig)
		serverGroup.GET("/users", serverHandler.GetUsers)
		serverGroup.POST("/traffic", serverHandler.PostTraffic)
	}

	// Route groups registered here as features are implemented:
	//   (admin + server-facing already wired above)

	// User API: subscription token (no login) + user login/protected.
	userSvc := service.NewUserService(db, sysCfg)
	subSvc := service.NewSubscriptionService(db)

	// Turnstile captcha verification (no-op until an admin enables it via
	// system-config). Shared across the public + admin auth handlers.
	captchaSvc := service.NewCaptchaService(sysCfg)
	subH := handler.NewSubHandler(subSvc)
	userAuthH := handler.NewUserAuthHandler(authSvc, userSvc, captchaSvc, sysCfg)
	userH := handler.NewUserHandler(userSvc, subSvc, sysCfg)

	// Billing (plans + orders + traffic packages) services/handlers.
	planSvc := service.NewPlanService(db)
	payments := payment.NewRegistry(sysCfg.GetAll)
	alipay.Register(payments)
	wechat.Register(payments)
	stripe.Register(payments)
	orderSvc := service.NewOrderService(db, sysCfg, payments)
	planH := handler.NewPlanHandler(planSvc)
	orderH := handler.NewOrderHandler(orderSvc)
	trafficPkgSvc := service.NewTrafficPackageService(db)
	trafficPkgH := handler.NewTrafficPackageHandler(trafficPkgSvc)

	// Traffic service powers both the admin and user traffic endpoints.
	trafficSvc := service.NewTrafficService(db)
	userTrafficH := handler.NewUserTrafficHandler(trafficSvc)

	// Invite / Announcement / Email services. The invite + email services are
	// wired onto AuthService so registration can validate invite codes and send
	// verification mail. sysCfg is the shared runtime-config instance.
	inviteSvc := service.NewInviteService(db, sysCfg)
	redemptionSvc := service.NewRedemptionService(db)
	emailSvc := service.NewEmailService(sysCfg)
	annSvc := service.NewAnnouncementService(db)
	authSvc.SetInviteService(inviteSvc)
	authSvc.SetEmailService(emailSvc)

	// TelegramService is the bot. It is constructed here (after userSvc /
	// annSvc / orderSvc / subSvc exist) with the node / stats services
	// injected a moment later via SetNodeService / SetStatsService (they
	// are created in the admin section below). It is wired back into the
	// services that emit admin alerts, and its user link handler is
	// registered into the userProtected group below.
	telegramSvc := service.NewTelegramService(db, sysCfg, authSvc, userSvc, annSvc, orderSvc, nil, nil, subSvc)
	authSvc.SetTelegramService(telegramSvc)
	orderSvc.SetTelegramService(telegramSvc)
	annSvc.SetTelegramService(telegramSvc)
	telegramUserH := handler.NewTelegramUserHandler(telegramSvc)

	// Ticket service: users open tickets and reply; admins reply + change
	// status. Notification services are wired in so an admin reply can reach
	// the ticket owner via email / Telegram (both no-op when unset).
	ticketSvc := service.NewTicketService(db)
	ticketSvc.SetEmailService(emailSvc)
	ticketSvc.SetTelegramService(telegramSvc)
	// Let the bot reply to tickets from Telegram by quoting the notification.
	telegramSvc.SetTicketService(ticketSvc)
	telegramSvc.Run()

	// User-facing invite + announcement handlers (used in the userProtected group).
	userInviteH := handler.NewUserInviteHandler(inviteSvc)
	userRedemptionH := handler.NewUserRedemptionHandler(redemptionSvc)
	userAnnH := handler.NewUserAnnouncementHandler(annSvc)

	// Public payment-gateway async-notification endpoints (unauthenticated,
	// like /sub). The platform is the URL segment, e.g. /api/v1/billing/alipay/notify.
	r.POST("/api/v1/billing/:platform/notify", orderH.Notify)

	// Rate-limit the unauthenticated endpoints (10 req/min/IP) to blunt abuse.
	loginLimit := middleware.RateLimit(10, 10)
	r.GET("/api/v1/user/config", loginLimit, userAuthH.GetConfig)
	r.GET("/api/v1/sub/:sub_token", loginLimit, subH.Subscribe)
	r.POST("/api/v1/user/login", loginLimit, userAuthH.Login)
	r.POST("/api/v1/user/register", loginLimit, userAuthH.Register)
	r.POST("/api/v1/user/verify-email", loginLimit, userAuthH.VerifyEmail)
	r.POST("/api/v1/user/resend-verification", loginLimit, userAuthH.ResendVerification)
	userProtected := r.Group("/api/v1/user")
	userProtected.Use(middleware.RequireUser(authSvc))
	{
		userProtected.GET("/profile", userH.Profile)
		userProtected.PUT("/profile", userH.UpdateProfile)
		userProtected.GET("/subscribe", userH.Subscribe)
		userProtected.GET("/subscribe-url", userH.SubscribeURL)
		userProtected.GET("/nodes", userH.Nodes)
		userProtected.POST("/regenerate-credential", userH.RegenerateCredential)
		userProtected.POST("/reset-sub-token", userH.ResetSubToken)

		userProtected.GET("/plans", planH.ListActive)
		userProtected.GET("/traffic-packages", trafficPkgH.ListActive)
		userProtected.POST("/orders", orderH.Create)
		userProtected.GET("/orders", orderH.ListMine)
		userProtected.GET("/orders/:id", orderH.GetMine)
		userProtected.POST("/orders/:id/pay", orderH.PayMine)
		userProtected.POST("/orders/:id/close", orderH.CloseMine)

		userProtected.GET("/traffic", userTrafficH.List)
		userProtected.GET("/traffic/hourly", userTrafficH.Hourly)

		userProtected.POST("/change-password", userAuthH.ChangePassword)

		// Invite codes owned by the caller, plus their remaining quota.
		userProtected.GET("/invites/status", userInviteH.Status)
		userProtected.GET("/invites", userInviteH.ListMine)
		userProtected.POST("/invites", userInviteH.Create)
		userProtected.DELETE("/invites/:id", userInviteH.Delete)

		// Redemption codes: redeem a code + view own redemption history.
		userProtected.POST("/redemption-codes/redeem", userRedemptionH.Redeem)
		userProtected.GET("/redemption-codes/records", userRedemptionH.Records)

		// Active announcements for the user SPA.
		userProtected.GET("/announcements", userAnnH.List)

		// Tickets: users open, list, view, and reply to their own tickets.
		userTicketH := handler.NewUserTicketHandler(ticketSvc)
		userProtected.GET("/tickets", userTicketH.List)
		userProtected.POST("/tickets", userTicketH.Create)
		userProtected.GET("/tickets/:id", userTicketH.Get)
		userProtected.POST("/tickets/:id/messages", userTicketH.Reply)
		userProtected.POST("/tickets/:id/close", userTicketH.Close)
		userProtected.GET("/tickets/unread", userTicketH.Unread)

		// Telegram link management: status, one-time bind code, unlink,
		// and announcement opt-in toggle.
		userProtected.GET("/telegram/status", telegramUserH.Status)
		userProtected.POST("/telegram/bind", telegramUserH.Bind)
		userProtected.POST("/telegram/unbind", telegramUserH.Unbind)
		userProtected.PUT("/telegram/notify", telegramUserH.SetNotify)
	}

	// Admin API.
	nodeSvc := service.NewNodeService(db)
	statsSvc := service.NewStatsService(db)

	// Inject the node / stats services into the Telegram bot now that
	// they exist; its /anodes and /astats commands need them. (The
	// bot lifecycle was already started above; this only supplies deps.)
	telegramSvc.SetNodeService(nodeSvc)
	telegramSvc.SetStatsService(statsSvc)

	adminAuthH := handler.NewAdminAuthHandler(authSvc, captchaSvc, sysCfg)
	adminNodeH := handler.NewAdminNodeHandler(nodeSvc)
	adminUserH := handler.NewAdminUserHandler(userSvc)
	adminAdminH := handler.NewAdminAdminHandler(authSvc)
	adminTrafficH := handler.NewAdminTrafficHandler(trafficSvc)
	adminStatsH := handler.NewAdminStatsHandler(statsSvc)
	systemH := handler.NewSystemHandler(sysCfg, srv)
	adminUtilH := handler.NewAdminUtilHandler()

	// Invite / Announcement / Email handlers (admin surface).
	adminInviteH := handler.NewAdminInviteHandler(inviteSvc)
	adminRedemptionH := handler.NewAdminRedemptionHandler(redemptionSvc)
	adminAnnH := handler.NewAdminAnnouncementHandler(annSvc)
	adminEmailH := handler.NewAdminEmailHandler(emailSvc, annSvc, db)
	adminTelegramH := handler.NewAdminTelegramHandler(telegramSvc, annSvc)

	admin := r.Group("/api/v1/admin")
	{
		// Public (unauthenticated, rate-limited) config consumed by the login
		// page — currently just the captcha knobs. Routed through adminAuthH so
		// the SPA can decide whether to render the Turnstile widget.
		admin.GET("/config", loginLimit, adminAuthH.GetConfig)
		admin.POST("/login", loginLimit, adminAuthH.Login)
		admin.POST("/refresh", adminAuthH.Refresh)
	}
	adminAuth := admin.Group("")
	adminAuth.Use(middleware.RequireAdmin(authSvc))
	{
		adminAuth.GET("/nodes", adminNodeH.List)
		adminAuth.POST("/nodes", adminNodeH.Create)
		adminAuth.GET("/nodes/:id", adminNodeH.Get)
		adminAuth.PUT("/nodes/:id", adminNodeH.Update)
		adminAuth.DELETE("/nodes/:id", adminNodeH.Delete)
		adminAuth.POST("/nodes/:id/regenerate-token", adminNodeH.RegenerateToken)
		adminAuth.GET("/nodes/:id/users", adminUserH.ListUsersForNode)

		adminAuth.GET("/users", adminUserH.List)
		adminAuth.POST("/users", adminUserH.Create)
		adminAuth.GET("/users/:id", adminUserH.Get)
		adminAuth.PUT("/users/:id", adminUserH.Update)
		adminAuth.DELETE("/users/:id", adminUserH.Delete)
		adminAuth.POST("/users/:id/regenerate-sub-token", adminUserH.RegenerateSubToken)
		adminAuth.POST("/users/:id/regenerate-credential", adminUserH.RegenerateCredential)
		adminAuth.PUT("/users/:id/password", adminUserH.SetPassword)
		adminAuth.GET("/users/:id/nodes", adminUserH.ListNodes)
		adminAuth.PUT("/users/:id/nodes", adminUserH.SetNodes)

		adminAuth.GET("/traffic", adminTrafficH.List)
		adminAuth.GET("/stats/overview", adminStatsH.Overview)
		adminAuth.POST("/utils/generate-x25519", adminUtilH.GenerateX25519)

		// Invite codes (admin may mint without quota; list/delete all codes).
		adminAuth.GET("/invites", adminInviteH.List)
		adminAuth.POST("/invites", adminInviteH.Create)
		adminAuth.DELETE("/invites/:id", adminInviteH.Delete)

		// Redemption codes (admin batch-generates; list/delete all; view records).
		adminAuth.GET("/redemption-codes", adminRedemptionH.List)
		adminAuth.POST("/redemption-codes", adminRedemptionH.Generate)
		adminAuth.GET("/redemption-codes/:id/records", adminRedemptionH.Records)
		adminAuth.DELETE("/redemption-codes/:id", adminRedemptionH.Delete)

		// Announcements (full CRUD; users see only active ones).
		adminAuth.GET("/announcements", adminAnnH.List)
		adminAuth.POST("/announcements", adminAnnH.Create)
		adminAuth.PUT("/announcements/:id", adminAnnH.Update)
		adminAuth.DELETE("/announcements/:id", adminAnnH.Delete)

		// Tickets: admins list/view all tickets, reply, and change status.
		adminTicketH := handler.NewAdminTicketHandler(ticketSvc)
		adminAuth.GET("/tickets", adminTicketH.List)
		adminAuth.GET("/tickets/:id", adminTicketH.Get)
		adminAuth.POST("/tickets/:id/messages", adminTicketH.Reply)
		adminAuth.PUT("/tickets/:id/status", adminTicketH.SetStatus)
		adminAuth.GET("/tickets/unread", adminTicketH.Unread)

		// Broadcast email to users (optionally also as an announcement).
		adminAuth.POST("/email/send", adminEmailH.Send)
		// Send a single test email to verify connectivity (uses saved config).
		adminAuth.POST("/email/test", adminEmailH.Test)

		// Telegram broadcast to linked users (optionally also an announcement).
		adminAuth.POST("/telegram/broadcast", adminTelegramH.Broadcast)

		// Admin self-service Telegram link, so an admin can reply to tickets
		// from the bot. The link flow mirrors the user bind flow (/astart).
		adminTelegramSelfH := handler.NewTelegramAdminHandler(telegramSvc)
		adminAuth.GET("/me/telegram/status", adminTelegramSelfH.Status)
		adminAuth.POST("/me/telegram/bind", adminTelegramSelfH.Bind)
		adminAuth.POST("/me/telegram/unbind", adminTelegramSelfH.Unbind)

		adminAuth.POST("/change-password", adminAuthH.ChangePassword)

		adminAuth.GET("/plans", planH.ListAll)
		adminAuth.POST("/orders", orderH.AdminCreate)
		adminAuth.GET("/orders", orderH.List)
		adminAuth.GET("/orders/:id", orderH.Get)
		adminAuth.PUT("/orders/:id/status", orderH.AdminUpdateStatus)

		// Products (plans + traffic packages): any admin may view, but only
		// super admins may create/update/delete (see superAuth group below).
		adminAuth.GET("/plans/:id", planH.Get)
		adminAuth.GET("/traffic-packages", trafficPkgH.ListAll)
		adminAuth.GET("/traffic-packages/:id", trafficPkgH.Get)
	}
	superAuth := admin.Group("")
	superAuth.Use(middleware.RequireSuperAdmin(authSvc))
	{
		superAuth.GET("/admins", adminAdminH.List)
		superAuth.POST("/admins", adminAdminH.Create)
		superAuth.GET("/admins/:id", adminAdminH.Get)
		superAuth.PUT("/admins/:id", adminAdminH.Update)
		superAuth.DELETE("/admins/:id", adminAdminH.Delete)
		superAuth.PUT("/admins/:id/password", adminAdminH.UpdatePassword)

		superAuth.POST("/plans", planH.Create)
		superAuth.PUT("/plans/:id", planH.Update)
		superAuth.DELETE("/plans/:id", planH.Delete)

		superAuth.POST("/traffic-packages", trafficPkgH.Create)
		superAuth.PUT("/traffic-packages/:id", trafficPkgH.Update)
		superAuth.DELETE("/traffic-packages/:id", trafficPkgH.Delete)

		// System config is super-admin only.
		superAuth.GET("/system-config", systemH.Get)
		superAuth.PUT("/system-config", systemH.Update)
	}

	return r
}
