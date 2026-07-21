// Package cmd provides the cobra CLI for the vgate manager.
package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/vgate-project/vgate-manager/config"
	"github.com/vgate-project/vgate-manager/internal/api"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
	"github.com/vgate-project/vgate-manager/internal/payment/alipay"
	"github.com/vgate-project/vgate-manager/internal/payment/stripe"
	"github.com/vgate-project/vgate-manager/internal/payment/wechat"
	"github.com/vgate-project/vgate-manager/internal/service"

	"github.com/glebarez/sqlite"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var cfgFile string
var captchaEnabled bool

var rootCmd = &cobra.Command{
	Use:   "vgate-manager",
	Short: "vgate manager API server",
	Run: func(cmd *cobra.Command, args []string) {
		run(cmd)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./config.yml)")
	rootCmd.PersistentFlags().BoolVar(&captchaEnabled, "captcha-enabled", false, "enable Cloudflare Turnstile captcha on auth endpoints (overrides DB setting only when explicitly set)")
}

// Execute is the single entry point called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initial logger from config.yml defaults; re-applied below once DB-backed
	// overrides (log.level / log.format) are loaded.
	applyLogger(cfg.Log)

	db, err := initDB(cfg)
	if err != nil {
		log.Fatalf("failed to init database: %v", err)
	}

	if err := db.AutoMigrate(
		&model.Admin{},
		&model.Node{},
		&model.User{},
		&model.UserNode{},
		&model.UserNodeTraffic{},
		&model.TrafficHourlyStat{},
		&model.RefreshToken{},
		&model.SystemConfig{},
		&model.InviteCode{},
		&model.EmailVerification{},
		&model.RedemptionCode{},
		&model.RedemptionRecord{},
		&model.Announcement{},
		&model.Plan{},
		&model.PlanPrice{},
		&model.TrafficPackage{},
		&model.Order{},
		&model.Ticket{},
		&model.TicketMessage{},
	); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	// migrations must be called in order
	migrations(db)

	// Merge DB-backed runtime config overrides on top of config.yml defaults.
	// If the DB read fails we fall back to config.yml so the service still
	// starts; the failure is logged but non-fatal. ApplyOverrides also writes
	// any missing migrated key's DefaultConfig value back into the database.
	sysCfg := service.NewSystemConfigService(db)
	merged := cfg
	if dbCfg, err := sysCfg.ApplyOverrides(cfg); err != nil {
		log.Warnf("system-config override failed, using config.yml defaults: %v", err)
	} else {
		merged = dbCfg
	}
	// Re-apply logger now that DB overrides for log.level / log.format are known.
	applyLogger(merged.Log)

	// An explicit --captcha-enabled flag overrides the DB-backed captcha switch
	// at startup; when the flag is omitted we leave the runtime/existing DB
	// value untouched so an admin can still toggle it live.
	if cmd.PersistentFlags().Changed("captcha-enabled") {
		if err := sysCfg.SetAll(map[string]string{
			service.CfgKeyCaptchaTurnstileEnabled: strconv.FormatBool(captchaEnabled),
		}); err != nil {
			log.Warnf("failed to apply --captcha-enabled flag: %v", err)
		} else {
			state := "disabled"
			if captchaEnabled {
				state = "enabled"
			}
			log.Infof("captcha (Turnstile) %s via --captcha-enabled flag", state)
		}
	}

	// Auth service + first-run admin bootstrap (config-seeded, idempotent).
	authSvc := service.NewAuthService(db, merged.JWT.Secret,
		time.Duration(merged.JWT.AccessTTLSecs)*time.Second,
		time.Duration(merged.JWT.RefreshTTLSecs)*time.Second)
	authSvc.SetConfigService(sysCfg)
	if pw, err := authSvc.BootstrapAdmin(merged.Admin.Bootstrap.Username, merged.Admin.Bootstrap.Password); err != nil {
		log.Errorf("admin bootstrap: %v", err)
	} else if pw != "" {
		log.Warnf("=================================================================")
		log.Warnf(" INITIAL ADMIN CREATED — username: %s  password: %s", merged.Admin.Bootstrap.Username, pw)
		log.Warnf(" This password is shown ONLY ONCE. Save it now; it cannot be recovered.")
		log.Warnf("=================================================================")
	} else {
		log.Infof("admin bootstrap complete (default admin: %s)", merged.Admin.Bootstrap.Username)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", merged.Server.Port),
		ReadTimeout:  time.Duration(merged.Server.ReadTimeoutSecs) * time.Second,
		WriteTimeout: time.Duration(merged.Server.WriteTimeoutSecs) * time.Second,
	}
	r := api.NewRouter(db, merged, authSvc, sysCfg, srv)
	srv.Handler = r

	// Periodically close orders that were never paid (alipay also auto-closes,
	// but this guarantees the local status reflects reality). Safe to re-run:
	// it only flips pending→closed and never grants benefits.
	payments := payment.NewRegistry(sysCfg.GetAll)
	alipay.Register(payments)
	wechat.Register(payments)
	stripe.Register(payments)
	orderSvc := service.NewOrderService(db, sysCfg, payments)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := orderSvc.CloseExpired(); err != nil {
				log.Errorf("closing expired orders: %v", err)
			} else if n > 0 {
				log.Infof("closed %d expired orders", n)
			}
		}
	}()

	// Clean up old hourly stats: they're only needed for the last 48h.
	statsSvc := service.NewStatsService(db)
	go func() {
		if err := statsSvc.DeleteOldHourlyStats(); err != nil {
			log.Errorf("initial stats aggregation: %v", err)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := statsSvc.DeleteOldHourlyStats(); err != nil {
				log.Errorf("hourly stats aggregation: %v", err)
			}
		}
	}()

	// Daily quota reset: users opted into the global monthly reset get their
	// usage counters zeroed on the configured reset day (system_config
	// quota.reset_day), renewing their quota window. Runs once at startup, then
	// every 24h; the day only matches once per month, so it can't double-reset.
	userSvc := service.NewUserService(db, sysCfg)
	go func() {
		runQuotaReset(userSvc, sysCfg)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runQuotaReset(userSvc, sysCfg)
		}
	}()

	log.Infof("vgate manager listening on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

// applyLogger configures the global logrus logger from the given LogConfig.
func applyLogger(lc config.LogConfig) {
	level, err := log.ParseLevel(lc.Level)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)
	if lc.Format == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	} else {
		log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
	}
}

// runQuotaReset resets monthly usage for users opted into the global reset, but
// only on the configured reset day (system_config quota.reset_day). It reads
// the day fresh each tick so an admin change applies on the next run.
func runQuotaReset(svc *service.UserService, sysCfg *service.SystemConfigService) {
	dayStr, err := sysCfg.Get(service.CfgKeyQuotaResetDay)
	if err != nil {
		// Not configured yet; skip until an admin sets it.
		return
	}
	day, err := strconv.Atoi(dayStr)
	if err != nil || day < 1 || day > 28 {
		log.Warnf("invalid %s=%q, skip quota reset", service.CfgKeyQuotaResetDay, dayStr)
		return
	}
	if time.Now().Day() != day {
		return
	}
	n, err := svc.ResetDueQuotas()
	if err != nil {
		log.Errorf("quota reset: %v", err)
	} else if n > 0 {
		log.Infof("reset monthly quota for %d users (day %d)", n, day)
	}
}

// migrateQuotaResetDay, migrateUserCredential, migrateCurrentProductID and
// migrateCurrentProductKind live in cmd/migrate.go.

func initDB(cfg *config.Config) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.DB.Dialect {
	case "postgres":
		dialector = postgres.Open(cfg.DB.DSN)
	default:
		dialector = sqlite.Open(cfg.DB.DSN)
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.DB.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DB.MaxIdleConns)
	return db, nil
}
