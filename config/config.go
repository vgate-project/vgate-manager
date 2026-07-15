// Package config loads the manager's runtime configuration via viper.
// Mirrors the server's config-loading style (file + env overrides).
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server ServerConfig
	DB     DBConfig
	JWT    JWTConfig
	Admin  AdminConfig
	Log    LogConfig
	CORS   CORSConfig
}

type ServerConfig struct {
	Port             int
	ReadTimeoutSecs  int
	WriteTimeoutSecs int
}

type DBConfig struct {
	Dialect      string // "sqlite" | "postgres"
	DSN          string
	MaxOpenConns int
	MaxIdleConns int
}

type JWTConfig struct {
	Secret         string
	AccessTTLSecs  int
	RefreshTTLSecs int
}

type AdminConfig struct {
	Bootstrap BootstrapAdmin
}

type BootstrapAdmin struct {
	Username string
	Password string
}

type LogConfig struct {
	Level  string
	Format string
}

type CORSConfig struct {
	AllowedOrigins []string
}

// DefaultConfig returns the hardcoded default runtime configuration. The
// DB-backed (hot-reloadable) sections — jwt TTLs, log, cors, and the server
// read/write timeouts — use these as their seed/default values in the database
// and are NOT overridden by config.yml file values at runtime. server.port,
// db.*, jwt.secret and admin.bootstrap are NOT migrated to the database:
// server.port/db are startup/infra config sourced from config.yml / env (db
// must exist before the DB can be read; port requires a listener rebind i.e. a
// restart, so it is not hot-reloadable; secret stays out of the DB by
// convention).
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:             8081,
			ReadTimeoutSecs:  30,
			WriteTimeoutSecs: 30,
		},
		JWT: JWTConfig{
			Secret:         "change-me-in-production",
			AccessTTLSecs:  7200,
			RefreshTTLSecs: 604800,
		},
		Admin: AdminConfig{
			Bootstrap: BootstrapAdmin{
				Username: "admin",
				Password: "change-me",
			},
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
	}
}

// Load reads the config file (path, or ./config.yml by default) and applies
// env overrides. Missing config file is acceptable — defaults/env apply.
//
// Note: server.port, db.*, jwt.secret and admin.bootstrap.* are sourced from
// this file (startup/infra config). The jwt-ttl/log/cors/server-timeout
// sections are seeded into the database and managed there at runtime; values
// present in config.yml for those sections are ignored.
func Load(path string) (*Config, error) {
	v := viper.New()
	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yml")
		v.AddConfigPath(".")
	}

	d := DefaultConfig()
	v.SetDefault("server.port", d.Server.Port)
	v.SetDefault("server.read_timeout_secs", d.Server.ReadTimeoutSecs)
	v.SetDefault("server.write_timeout_secs", d.Server.WriteTimeoutSecs)
	v.SetDefault("db.dialect", "sqlite")
	v.SetDefault("db.dsn", "vgate_manager.db")
	v.SetDefault("db.max_open_conns", 20)
	v.SetDefault("db.max_idle_conns", 5)
	v.SetDefault("jwt.secret", d.JWT.Secret)
	v.SetDefault("jwt.access_ttl_secs", d.JWT.AccessTTLSecs)
	v.SetDefault("jwt.refresh_ttl_secs", d.JWT.RefreshTTLSecs)
	v.SetDefault("admin.bootstrap.username", d.Admin.Bootstrap.Username)
	v.SetDefault("admin.bootstrap.password", d.Admin.Bootstrap.Password)
	v.SetDefault("log.level", d.Log.Level)
	v.SetDefault("log.format", d.Log.Format)
	v.SetDefault("cors.allowed_origins", d.CORS.AllowedOrigins)

	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
		// config file missing is acceptable
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}
