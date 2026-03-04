package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/cosmobean/runic/internal/auth"
	"github.com/cosmobean/runic/internal/config"
	"github.com/cosmobean/runic/internal/daemon"
	"github.com/cosmobean/runic/internal/oauth"
	"github.com/cosmobean/runic/internal/pair"
	"github.com/cosmobean/runic/internal/server"
	"github.com/cosmobean/runic/internal/service"
)

var version = "0.1.0"

func main() {
	root := &cobra.Command{
		Use:   "runic",
		Short: "Your terminals. Everywhere.",
	}

	// runic start
	var startDetach bool
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Runic daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			if startDetach {
				binaryPath, err := os.Executable()
				if err != nil {
					return fmt.Errorf("resolve current executable path: %w", err)
				}
				if err := daemon.StartDetached(daemon.StartOpts{
					BinaryPath: binaryPath,
					ConfigPath: cfgPath,
				}); err != nil {
					return err
				}
				status, _ := daemon.Status("", "")
				fmt.Printf("[ok] Detached daemon started (pid %d)\n", status.PID)
				fmt.Printf("pid file: %s\n", status.PIDPath)
				fmt.Printf("log file: %s\n", status.LogPath)
				return nil
			}

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
	startCmd.Flags().BoolVar(&startDetach, "detach", false, "Run daemon in background and return")
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

			cfgPath := config.ConfigPath()
			resetToken, _ := cmd.Flags().GetBool("reset-token")

			// Create new config if missing.
			if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
				token, err := pair.GenerateToken()
				if err != nil {
					return err
				}
				hash := auth.HashToken(token)
				cfg := config.DefaultConfig()
				cfg.Auth.TokenHash = hash

				if err := writeConfig(cfgPath, cfg); err != nil {
					return err
				}

				fmt.Printf("[ok] Config written to %s\n", cfgPath)
				fmt.Printf("[ok] Auth token (save this, shown once):\n\n  %s\n\n", token)
				return nil
			}

			// Existing config.
			if !resetToken {
				fmt.Printf("Config already exists at %s\n", cfgPath)
				fmt.Println("No new token was generated.")
				fmt.Println("If you need a new token, run: runic setup --reset-token")
				return nil
			}

			token, err := pair.GenerateToken()
			if err != nil {
				return err
			}
			hash := auth.HashToken(token)

			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			cfg.Auth.TokenHash = hash
			if err := writeConfig(cfgPath, cfg); err != nil {
				return err
			}

			fmt.Printf("[ok] Auth token reset and config updated at %s\n", cfgPath)
			fmt.Printf("[ok] Auth token (save this, shown once):\n\n  %s\n\n", token)
			return nil
		},
	}
	setupCmd.Flags().Bool("reset-token", false, "Generate and store a new auth token hash in config")
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

	// runic status
	var statusConfigPath string
	var statusAsJSON bool
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon readiness summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(statusConfigPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			svcOut, svcErr := service.Status()
			tmuxPath := findBinary("tmux")

			type serviceSummary struct {
				OK     bool   `json:"ok"`
				Status string `json:"status,omitempty"`
				Error  string `json:"error,omitempty"`
			}
			type statusSummary struct {
				Version    string         `json:"version"`
				ConfigPath string         `json:"config_path"`
				Machine    string         `json:"machine"`
				ServerHost string         `json:"server_host"`
				ServerPort int            `json:"server_port"`
				TLSMode    string         `json:"tls_mode"`
				RequireTok bool           `json:"require_token"`
				TokenSet   bool           `json:"token_hash_set"`
				Origins    []string       `json:"allowed_origins"`
				TrustProxy bool           `json:"trust_proxy_headers"`
				TmuxPath   string         `json:"tmux_path,omitempty"`
				Service    serviceSummary `json:"service"`
			}

			summary := statusSummary{
				Version:    version,
				ConfigPath: statusConfigPath,
				Machine:    cfg.Machine.Name,
				ServerHost: cfg.Server.Host,
				ServerPort: cfg.Server.Port,
				TLSMode:    cfg.TLS.Mode,
				RequireTok: cfg.Auth.RequireToken,
				TokenSet:   cfg.Auth.TokenHash != "",
				Origins:    cfg.Security.AllowedOrigins,
				TrustProxy: cfg.Security.TrustProxyHeaders,
				TmuxPath:   tmuxPath,
				Service: serviceSummary{
					OK:     svcErr == nil,
					Status: svcOut,
				},
			}
			if svcErr != nil {
				summary.Service.Error = svcErr.Error()
			}

			if statusAsJSON {
				data, err := json.MarshalIndent(summary, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("version: %s\n", summary.Version)
			fmt.Printf("machine: %s\n", summary.Machine)
			fmt.Printf("config: %s\n", summary.ConfigPath)
			fmt.Printf("listen: %s:%d (tls=%s)\n", summary.ServerHost, summary.ServerPort, summary.TLSMode)
			fmt.Printf("auth: require_token=%t token_hash_set=%t\n", summary.RequireTok, summary.TokenSet)
			fmt.Printf("security: allowed_origins=%d trust_proxy_headers=%t\n", len(summary.Origins), summary.TrustProxy)
			if summary.TmuxPath != "" {
				fmt.Printf("tmux: available (%s)\n", summary.TmuxPath)
			} else {
				fmt.Println("tmux: not found")
			}
			if summary.Service.Status != "" {
				fmt.Println("service:")
				fmt.Println(summary.Service.Status)
			}
			if svcErr != nil {
				return svcErr
			}
			return nil
		},
	}
	statusCmd.Flags().StringVar(&statusConfigPath, "config", config.ConfigPath(), "Path to config file")
	statusCmd.Flags().BoolVar(&statusAsJSON, "json", false, "Output status as JSON")
	root.AddCommand(statusCmd)

	// runic token
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Manage daemon auth token",
	}
	var tokenConfigPath string
	tokenCmd.PersistentFlags().StringVar(&tokenConfigPath, "config", config.ConfigPath(), "Path to config file")
	tokenCmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Rotate daemon auth token and update config hash",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(tokenConfigPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			token, err := pair.GenerateToken()
			if err != nil {
				return err
			}
			cfg.Auth.TokenHash = auth.HashToken(token)
			if err := writeConfig(tokenConfigPath, cfg); err != nil {
				return err
			}
			fmt.Printf("[ok] Auth token rotated and config updated at %s\n", tokenConfigPath)
			fmt.Printf("[ok] Auth token (save this, shown once):\n\n  %s\n\n", token)
			return nil
		},
	})
	root.AddCommand(tokenCmd)

	// runic doctor
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run deployment readiness diagnostics",
	}
	var doctorConfigPath string
	doctorCmd.PersistentFlags().StringVar(&doctorConfigPath, "config", config.ConfigPath(), "Path to config file")
	doctorCmd.AddCommand(&cobra.Command{
		Use:   "internet",
		Short: "Check internet-facing hardening readiness",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(doctorConfigPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			type check struct {
				Name     string
				OK       bool
				Required bool
				Detail   string
			}
			checks := []check{
				{
					Name:     "localhost bind",
					OK:       isLocalHost(cfg.Server.Host),
					Required: true,
					Detail:   fmt.Sprintf("server.host=%s", cfg.Server.Host),
				},
				{
					Name:     "token required",
					OK:       cfg.Auth.RequireToken,
					Required: true,
					Detail:   fmt.Sprintf("auth.require_token=%t", cfg.Auth.RequireToken),
				},
				{
					Name:     "token configured",
					OK:       cfg.Auth.TokenHash != "",
					Required: true,
					Detail:   "auth.token_hash must be set",
				},
				{
					Name:     "allowed origins set",
					OK:       len(cfg.Security.AllowedOrigins) > 0,
					Required: true,
					Detail:   fmt.Sprintf("security.allowed_origins=%d", len(cfg.Security.AllowedOrigins)),
				},
				{
					Name:     "proxy header trust enabled",
					OK:       cfg.Security.TrustProxyHeaders,
					Required: true,
					Detail:   fmt.Sprintf("security.trust_proxy_headers=%t", cfg.Security.TrustProxyHeaders),
				},
				{
					Name:     "trusted proxy CIDRs configured",
					OK:       len(cfg.Security.TrustedProxyCIDRs) > 0,
					Required: true,
					Detail:   fmt.Sprintf("security.trusted_proxy_cidrs=%d", len(cfg.Security.TrustedProxyCIDRs)),
				},
				{
					Name:     "tmux availability",
					OK:       findBinary("tmux") != "",
					Required: false,
					Detail:   "recommended for persistent sessions",
				},
			}

			if svcOut, svcErr := service.Status(); svcErr == nil {
				checks = append(checks, check{
					Name:     "service status",
					OK:       strings.Contains(strings.ToLower(svcOut), "state = running") || strings.Contains(strings.ToLower(svcOut), "active (running)"),
					Required: false,
					Detail:   "runic background service",
				})
			} else {
				checks = append(checks, check{
					Name:     "service status",
					OK:       false,
					Required: false,
					Detail:   svcErr.Error(),
				})
			}

			hardFail := false
			for _, c := range checks {
				prefix := "[ok]"
				if !c.OK && c.Required {
					prefix = "[fail]"
					hardFail = true
				} else if !c.OK {
					prefix = "[warn]"
				}
				fmt.Printf("%s %s: %s\n", prefix, c.Name, c.Detail)
			}

			if hardFail {
				return fmt.Errorf("internet doctor failed: fix required checks before public exposure")
			}
			fmt.Println("[ok] internet doctor passed required checks")
			return nil
		},
	})
	root.AddCommand(doctorCmd)

	// runic service <action>
	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Manage background Runic service (macOS launchd, Linux systemd --user)",
	}
	var serviceConfigPath string
	var serviceBinaryPath string
	serviceCmd.PersistentFlags().StringVar(&serviceConfigPath, "config", config.ConfigPath(), "Path to Runic config file")
	serviceCmd.PersistentFlags().StringVar(&serviceBinaryPath, "binary", "", "Path to runic binary (defaults to current executable)")

	resolveBinaryPath := func() (string, error) {
		if serviceBinaryPath != "" {
			return serviceBinaryPath, nil
		}
		p, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve current executable path: %w", err)
		}
		return p, nil
	}

	serviceCmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install and enable the Runic user service",
		RunE: func(cmd *cobra.Command, args []string) error {
			binaryPath, err := resolveBinaryPath()
			if err != nil {
				return err
			}
			if err := service.Install(binaryPath, serviceConfigPath); err != nil {
				return err
			}
			fmt.Println("[ok] Service installed and enabled")
			return nil
		},
	})

	serviceCmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the Runic service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := service.Start(); err != nil {
				return err
			}
			fmt.Println("[ok] Service started")
			return nil
		},
	})

	serviceCmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the Runic service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := service.Stop(); err != nil {
				return err
			}
			fmt.Println("[ok] Service stopped")
			return nil
		},
	})

	serviceCmd.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart the Runic service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := service.Restart(); err != nil {
				return err
			}
			fmt.Println("[ok] Service restarted")
			return nil
		},
	})

	serviceCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show Runic service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := service.Status()
			if out != "" {
				fmt.Println(out)
			}
			return err
		},
	})

	serviceCmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Disable and remove the Runic service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := service.Uninstall(); err != nil {
				return err
			}
			fmt.Println("[ok] Service uninstalled")
			return nil
		},
	})
	root.AddCommand(serviceCmd)

	// runic daemon <action>
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage detached Runic daemon process",
	}
	var daemonConfigPath string
	var daemonBinaryPath string
	var daemonPIDPath string
	var daemonLogPath string
	daemonCmd.PersistentFlags().StringVar(&daemonConfigPath, "config", config.ConfigPath(), "Path to Runic config file")
	daemonCmd.PersistentFlags().StringVar(&daemonBinaryPath, "binary", "", "Path to runic binary (defaults to current executable)")
	daemonCmd.PersistentFlags().StringVar(&daemonPIDPath, "pid-file", daemon.DefaultPIDPath(), "PID file for detached daemon")
	daemonCmd.PersistentFlags().StringVar(&daemonLogPath, "log-file", daemon.DefaultLogPath(), "Log file for detached daemon")

	resolveDaemonBinary := func() (string, error) {
		if daemonBinaryPath != "" {
			return daemonBinaryPath, nil
		}
		p, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve current executable path: %w", err)
		}
		return p, nil
	}

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start detached daemon in background",
		RunE: func(cmd *cobra.Command, args []string) error {
			binaryPath, err := resolveDaemonBinary()
			if err != nil {
				return err
			}
			if err := daemon.StartDetached(daemon.StartOpts{
				BinaryPath: binaryPath,
				ConfigPath: daemonConfigPath,
				PIDPath:    daemonPIDPath,
				LogPath:    daemonLogPath,
			}); err != nil {
				return err
			}
			status, _ := daemon.Status(daemonPIDPath, daemonLogPath)
			fmt.Printf("[ok] Detached daemon started (pid %d)\n", status.PID)
			fmt.Printf("pid file: %s\n", status.PIDPath)
			fmt.Printf("log file: %s\n", status.LogPath)
			return nil
		},
	})

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop detached daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := daemon.Stop(daemonPIDPath, 5*time.Second)
			if err == nil || err == daemon.ErrNotRunning {
				fmt.Println("[ok] Detached daemon stopped")
				return nil
			}
			return err
		},
	})

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart detached daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := daemon.Stop(daemonPIDPath, 5*time.Second); err != nil && err != daemon.ErrNotRunning {
				return err
			}
			binaryPath, err := resolveDaemonBinary()
			if err != nil {
				return err
			}
			if err := daemon.StartDetached(daemon.StartOpts{
				BinaryPath: binaryPath,
				ConfigPath: daemonConfigPath,
				PIDPath:    daemonPIDPath,
				LogPath:    daemonLogPath,
			}); err != nil {
				return err
			}
			status, _ := daemon.Status(daemonPIDPath, daemonLogPath)
			fmt.Printf("[ok] Detached daemon restarted (pid %d)\n", status.PID)
			return nil
		},
	})

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show detached daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := daemon.Status(daemonPIDPath, daemonLogPath)
			if err != nil {
				return err
			}
			fmt.Printf("running: %t\n", status.Running)
			fmt.Printf("pid: %d\n", status.PID)
			fmt.Printf("pid_file: %s\n", status.PIDPath)
			fmt.Printf("log_file: %s\n", status.LogPath)
			if !status.Detached {
				fmt.Println("note: detached daemon mode unsupported on this platform")
			}
			return nil
		},
	})

	root.AddCommand(daemonCmd)

	// runic oauth <provider>
	oauthCmd := &cobra.Command{
		Use:   "oauth",
		Short: "Authenticate via OAuth in the browser",
	}

	var oauthClientID string
	var oauthClientSecret string
	var oauthRedirectURL string
	var oauthNoBrowser bool
	var oauthTimeoutSec int
	var oauthPrintToken bool
	var oauthNoStore bool

	oauthCmd.PersistentFlags().StringVar(&oauthClientID, "client-id", "", "OAuth client ID (or provider env var)")
	oauthCmd.PersistentFlags().StringVar(&oauthClientSecret, "client-secret", "", "OAuth client secret (or provider env var)")
	oauthCmd.PersistentFlags().StringVar(&oauthRedirectURL, "redirect-url", "http://127.0.0.1:53682/callback", "Loopback callback URL configured in your OAuth app")
	oauthCmd.PersistentFlags().BoolVar(&oauthNoBrowser, "no-browser", false, "Do not auto-open a browser")
	oauthCmd.PersistentFlags().IntVar(&oauthTimeoutSec, "timeout", 180, "OAuth timeout in seconds")
	oauthCmd.PersistentFlags().BoolVar(&oauthPrintToken, "print-access-token", false, "Print OAuth access token to stdout")
	oauthCmd.PersistentFlags().BoolVar(&oauthNoStore, "no-store", false, "Do not store OAuth tokens in OS keychain")

	makeOAuthProviderCmd := func(provider string) *cobra.Command {
		return &cobra.Command{
			Use:   provider,
			Short: fmt.Sprintf("Login with %s OAuth", providerLabel(provider)),
			RunE: func(cmd *cobra.Command, args []string) error {
				clientID, clientSecret, err := resolveOAuthClientCreds(provider, oauthClientID, oauthClientSecret)
				if err != nil {
					return err
				}

				ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(oauthTimeoutSec)*time.Second)
				defer cancel()

				res, err := oauth.Login(ctx, oauth.LoginOpts{
					Provider:     provider,
					ClientID:     clientID,
					ClientSecret: clientSecret,
					RedirectURL:  oauthRedirectURL,
					OpenBrowser:  !oauthNoBrowser,
				})
				if err != nil {
					return err
				}

				if !oauthNoStore {
					if err := oauth.SaveStoredToken(oauth.StoredToken{
						Provider:     provider,
						AccountID:    res.AccountID,
						Login:        res.Login,
						Name:         res.Name,
						Email:        res.Email,
						AccessToken:  res.AccessToken,
						RefreshToken: res.RefreshToken,
						TokenType:    res.TokenType,
						Scope:        res.Scope,
						ExpiresAt:    res.ExpiresAt,
					}); err != nil {
						return fmt.Errorf("oauth login succeeded but secure token storage failed: %w", err)
					}
				}

				fmt.Printf("[ok] OAuth login complete (%s)\n", provider)
				fmt.Printf("account_id: %s\n", res.AccountID)
				fmt.Printf("login: %s\n", res.Login)
				fmt.Printf("name: %s\n", res.Name)
				fmt.Printf("email: %s\n", res.Email)
				fmt.Printf("scope: %s\n", res.Scope)
				if !res.ExpiresAt.IsZero() {
					fmt.Printf("expires_at: %s\n", res.ExpiresAt.Format(time.RFC3339))
				}
				if !oauthNoStore {
					fmt.Println("stored: keychain")
				} else {
					fmt.Println("stored: no")
				}
				if oauthPrintToken {
					fmt.Printf("access_token: %s\n", res.AccessToken)
				}
				return nil
			},
		}
	}

	oauthCmd.AddCommand(makeOAuthProviderCmd("github"))
	oauthCmd.AddCommand(makeOAuthProviderCmd("google"))

	oauthCmd.AddCommand(&cobra.Command{
		Use:   "refresh <provider>",
		Short: "Refresh stored OAuth tokens for a provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.ToLower(args[0])
			stored, err := oauth.LoadStoredToken(provider)
			if err != nil {
				if err == oauth.ErrNoStoredToken {
					return fmt.Errorf("no stored token for provider %s; run `runic oauth %s` first", provider, provider)
				}
				return err
			}
			if stored.RefreshToken == "" {
				return fmt.Errorf("stored token for %s has no refresh token; re-authenticate with `runic oauth %s`", provider, provider)
			}

			clientID, clientSecret, err := resolveOAuthClientCreds(provider, oauthClientID, oauthClientSecret)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(oauthTimeoutSec)*time.Second)
			defer cancel()

			refreshed, err := oauth.Refresh(ctx, oauth.RefreshOpts{
				Provider:     provider,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				RefreshToken: stored.RefreshToken,
			})
			if err != nil {
				return err
			}

			if !oauthNoStore {
				if err := oauth.SaveStoredToken(oauth.StoredToken{
					Provider:     provider,
					AccountID:    refreshed.AccountID,
					Login:        refreshed.Login,
					Name:         refreshed.Name,
					Email:        refreshed.Email,
					AccessToken:  refreshed.AccessToken,
					RefreshToken: refreshed.RefreshToken,
					TokenType:    refreshed.TokenType,
					Scope:        refreshed.Scope,
					ExpiresAt:    refreshed.ExpiresAt,
				}); err != nil {
					return fmt.Errorf("token refresh succeeded but secure storage update failed: %w", err)
				}
			}

			fmt.Printf("[ok] OAuth token refreshed (%s)\n", provider)
			fmt.Printf("login: %s\n", refreshed.Login)
			fmt.Printf("email: %s\n", refreshed.Email)
			fmt.Printf("scope: %s\n", refreshed.Scope)
			if !refreshed.ExpiresAt.IsZero() {
				fmt.Printf("expires_at: %s\n", refreshed.ExpiresAt.Format(time.RFC3339))
			}
			if oauthPrintToken {
				fmt.Printf("access_token: %s\n", refreshed.AccessToken)
			}
			return nil
		},
	})

	oauthCmd.AddCommand(&cobra.Command{
		Use:   "status <provider>",
		Short: "Show whether a provider token is stored",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.ToLower(args[0])
			stored, err := oauth.LoadStoredToken(provider)
			if err != nil {
				if err == oauth.ErrNoStoredToken {
					fmt.Printf("provider: %s\nstored: no\n", provider)
					return nil
				}
				return err
			}
			fmt.Printf("provider: %s\n", provider)
			fmt.Println("stored: yes")
			fmt.Printf("account_id: %s\n", stored.AccountID)
			fmt.Printf("login: %s\n", stored.Login)
			fmt.Printf("name: %s\n", stored.Name)
			fmt.Printf("email: %s\n", stored.Email)
			fmt.Printf("scope: %s\n", stored.Scope)
			if !stored.ExpiresAt.IsZero() {
				fmt.Printf("expires_at: %s\n", stored.ExpiresAt.Format(time.RFC3339))
			}
			fmt.Printf("saved_at: %s\n", stored.SavedAt.Format(time.RFC3339))
			fmt.Printf("refresh_token_present: %t\n", stored.RefreshToken != "")
			return nil
		},
	})

	oauthCmd.AddCommand(&cobra.Command{
		Use:   "logout <provider>",
		Short: "Delete stored provider OAuth token from keychain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.ToLower(args[0])
			if err := oauth.DeleteStoredToken(provider); err != nil {
				return err
			}
			fmt.Printf("[ok] Removed stored OAuth token for %s\n", provider)
			return nil
		},
	})

	root.AddCommand(oauthCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func envKeyForProvider(provider, key string) string {
	return "RUNIC_" + strings.ToUpper(provider) + "_" + key
}

func providerLabel(provider string) string {
	switch provider {
	case "github":
		return "GitHub"
	case "google":
		return "Google"
	default:
		return provider
	}
}

func resolveOAuthClientCreds(provider, clientID, clientSecret string) (string, string, error) {
	if clientID == "" {
		clientID = os.Getenv(envKeyForProvider(provider, "CLIENT_ID"))
	}
	if clientSecret == "" {
		clientSecret = os.Getenv(envKeyForProvider(provider, "CLIENT_SECRET"))
	}
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("%s client credentials missing: use --client-id/--client-secret or set %s and %s",
			provider,
			envKeyForProvider(provider, "CLIENT_ID"),
			envKeyForProvider(provider, "CLIENT_SECRET"),
		)
	}
	return clientID, clientSecret, nil
}

func writeConfig(path string, cfg *config.Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func findBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range []string{
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
		"/home/linuxbrew/.linuxbrew/bin/" + name,
		"/usr/bin/" + name,
		"/bin/" + name,
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}

func isLocalHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
