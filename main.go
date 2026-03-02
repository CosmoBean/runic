package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/cosmobean/runic/internal/auth"
	"github.com/cosmobean/runic/internal/config"
	"github.com/cosmobean/runic/internal/pair"
	"github.com/cosmobean/runic/internal/server"
)

var version = "0.1.0"

func main() {
	root := &cobra.Command{
		Use:   "runic",
		Short: "Your terminals. Everywhere.",
	}

	// runic start
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Runic daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle signals
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Println("Shutting down...")
				cancel()
			}()

			srv := server.New(cfg)
			return srv.Start(ctx)
		},
	}
	startCmd.Flags().String("config", config.ConfigPath(), "Path to config file")
	root.AddCommand(startCmd)

	// runic setup
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Initialize Runic configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := config.ConfigDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}

			token, err := pair.GenerateToken()
			if err != nil {
				return err
			}
			hash := auth.HashToken(token)

			cfgPath := config.ConfigPath()

			// Only create if doesn't exist
			if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
				cfg := config.DefaultConfig()
				cfg.Auth.TokenHash = hash

				// Write config
				f, err := os.OpenFile(cfgPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
				if err != nil {
					return err
				}
				defer f.Close()

				_, _ = fmt.Fprintf(f, "machine:\n  name: %s\n\n", cfg.Machine.Name)
				_, _ = fmt.Fprintf(f, "server:\n  host: %s\n  port: %d\n\n", cfg.Server.Host, cfg.Server.Port)
				_, _ = fmt.Fprintf(f, "auth:\n  token_hash: \"%s\"\n  rate_limit: %d\n  lockout_minutes: %d\n\n",
					hash, cfg.Auth.RateLimit, cfg.Auth.LockoutMin)
				_, _ = fmt.Fprintf(f, "tls:\n  mode: self-signed\n\n")
				_, _ = fmt.Fprintf(f, "sessions:\n  max_sessions: %d\n", cfg.Sessions.MaxSessions)

				fmt.Printf("[ok] Config written to %s\n", cfgPath)
			} else {
				fmt.Printf("Config already exists at %s\n", cfgPath)
			}

			fmt.Printf("[ok] Auth token (save this, shown once):\n\n  %s\n\n", token)
			return nil
		},
	}
	root.AddCommand(setupCmd)

	// runic pair
	pairCmd := &cobra.Command{
		Use:   "pair",
		Short: "Show QR code to pair with mobile app",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			token, err := pair.GenerateToken()
			if err != nil {
				return err
			}

			// In a full implementation, this would register the token
			// with the running daemon. For Phase 1, just display it.
			url := pair.PairURL("YOUR_IP", cfg.Server.Port, token, cfg.Machine.Name)

			fmt.Println("Scan this QR code with the Runic app:")
			_ = pair.GenerateQR(url)
			fmt.Printf("\nOr enter manually: %s\n", url)
			fmt.Printf("\nPairing token: %s\n", token)
			return nil
		},
	}
	pairCmd.Flags().String("config", config.ConfigPath(), "Path to config file")
	root.AddCommand(pairCmd)

	// runic version
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("runic %s\n", version)
		},
	})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
