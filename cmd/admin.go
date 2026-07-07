package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/vgate-project/vgate-manager/config"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

var (
	adminUsername string
	adminPassword string
	adminRole     string
)

// adminCmd groups admin-account management subcommands.
var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Admin account management",
}

// adminCreateCmd implements `manager admin create --username X --password Y [--role super_admin]`.
var adminCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an admin account",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load(cfgFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
			os.Exit(1)
		}
		db, err := initDB(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to init database: %v\n", err)
			os.Exit(1)
		}
		if err := db.AutoMigrate(&model.Admin{}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to migrate: %v\n", err)
			os.Exit(1)
		}
		authSvc := service.NewAuthService(db, cfg.JWT.Secret,
			time.Duration(cfg.JWT.AccessTTLSecs)*time.Second,
			time.Duration(cfg.JWT.RefreshTTLSecs)*time.Second)
		admin, err := authSvc.CreateAdmin(adminUsername, adminPassword, adminRole)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create admin: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("created admin %q (id=%d, role=%s)\n", admin.Username, admin.ID, admin.Role)
	},
}

func init() {
	adminCreateCmd.Flags().StringVar(&adminUsername, "username", "", "admin username")
	adminCreateCmd.Flags().StringVar(&adminPassword, "password", "", "admin password")
	adminCreateCmd.Flags().StringVar(&adminRole, "role", "admin", "admin role: admin|super_admin")
	_ = adminCreateCmd.MarkFlagRequired("username")
	_ = adminCreateCmd.MarkFlagRequired("password")
	adminCmd.AddCommand(adminCreateCmd)
	rootCmd.AddCommand(adminCmd)
}
