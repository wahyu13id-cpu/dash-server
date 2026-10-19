// Command server runs the dashboard: an authenticated web UI with an
// interactive terminal and a sandboxed file manager.
//
// Security posture: this application grants shell access to its workspace and
// MUST only be exposed on a trusted network, or behind HTTPS/VPN (for example
// via the bundled nginx configuration). Do not publish it to the public
// internet without TLS and strong credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/db"
	"dashboard/internal/files"
	"dashboard/internal/httpapi"
	"dashboard/internal/sshaskpass"
	"dashboard/internal/sshsecret"
	"dashboard/internal/sshsvc"
	"dashboard/internal/sysinfo"
	"dashboard/internal/sysops"
	"dashboard/internal/terminal"
	"dashboard/internal/workspace"
	"dashboard/migrations"
)

func main() {
	// ssh exec()s SSH_ASKPASS with the prompt as the only argument, so if this
	// process was started by ssh it must never do anything else — in particular
	// it must never start the server. The marker is only present in ssh session
	// environments, so normal CLI use is unaffected.
	if sshaskpass.IsHelperInvocation() {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "askpass" {
			args = args[1:]
		}
		if len(args) == 0 {
			os.Exit(1)
		}
		os.Exit(runAskpass(args))
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-totp":
			if err := runResetTOTP(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fatal:", err)
				os.Exit(1)
			}
			return
		case "reset-ssh-key":
			if err := runResetSSHKey(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fatal:", err)
				os.Exit(1)
			}
			return
		case "askpass":
			// Manual/debug entry point for the same helper ssh calls. ssh
			// itself invokes the bare binary (see IsHelperInvocation above),
			// because SSH_ASKPASS is exec()d without a shell.
			os.Exit(runAskpass(os.Args[2:]))
		case "help", "-h", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`Usage:
  dashboard [command]

Commands:
  (none)                        start the HTTP server
  reset-totp --user <username>  disable two-factor authentication for an account
                                (break-glass when the phone and recovery codes
                                are both lost; run it on the server host)
  reset-ssh-key [--alias <a>]   delete every stored SSH credential, or only the
                                one for <a> (break-glass after rotating
                                DASH_SESSION_SECRET)
  askpass <prompt>              internal helper ssh calls via SSH_ASKPASS; not
                                meant to be run by hand

The server reads its configuration from the environment, falling back to a
.env file in the working directory (override with DASH_ENV_FILE).
`)
}

// runResetTOTP clears the TOTP secret and recovery codes of one account. It is
// the break-glass path for a locked-out operator and requires filesystem access
// to the database, i.e. SSH access to the host.
func runResetTOTP(args []string) error {
	var username string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--user", "-u":
			if i+1 < len(args) {
				username = strings.TrimSpace(args[i+1])
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") && username == "" {
				username = strings.TrimSpace(args[i])
			}
		}
	}
	if username == "" {
		return errors.New("usage: dashboard reset-totp --user <username>")
	}

	ctx := context.Background()
	path := config.ResolveDBPath()
	conn, err := db.Open(ctx, path)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	store := auth.NewStore(conn)
	userID, name, found, err := store.ResetTOTPByUsername(ctx, username)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no user named %q in %s", username, path)
	}

	audit.NewLogger(conn).Log(ctx, audit.Entry{
		UserID: userID, Username: name, Action: audit.ActionTOTPDisabled,
		Detail: "disabled via CLI (break-glass)", Success: true,
	})
	fmt.Printf("two-factor authentication disabled for %q (database: %s)\n", name, path)
	return nil
}

// runAskpass implements the SSH_ASKPASS helper that ssh invokes. The secret is
// fetched over the broker socket named in the environment and printed on
// stdout; nothing else is written and nothing is logged.
func runAskpass(args []string) int {
	prompt := ""
	if len(args) > 0 {
		prompt = strings.Join(args, " ")
	}
	secret, err := sshaskpass.Ask(prompt)
	if err != nil || secret == "" {
		return 1
	}
	fmt.Fprintln(os.Stdout, secret)
	return 0
}

// runResetSSHKey clears stored SSH credentials. It is the break-glass path for a
// rotated DASH_SESSION_SECRET, or for an operator who wants every stored
// password gone without editing the database by hand.
func runResetSSHKey(args []string) error {
	fs := flag.NewFlagSet("reset-ssh-key", flag.ContinueOnError)
	alias := fs.String("alias", "", "hapus kredensial untuk satu alias saja")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	path := config.ResolveDBPath()
	conn, err := db.Open(ctx, path)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	store := sshsecret.NewStore(conn)
	if *alias != "" {
		n, err := store.DeleteAlias(ctx, *alias)
		if err != nil {
			return err
		}
		fmt.Printf("menghapus %d kredensial untuk alias %q (database: %s)\n", n, *alias, path)
		return nil
	}
	n, err := store.PurgeAll(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("menghapus %d kredensial SSH tersimpan (database: %s)\n", n, path)
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	setupLogging(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	ws, err := workspace.New(cfg.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("initialize workspace: %w", err)
	}
	slog.Info("workspace ready", "root", ws.Root())

	auditLog := audit.NewLogger(conn)
	authStore := auth.NewStore(conn)
	authSvc := auth.NewService(authStore, auditLog, auth.Options{
		SessionTTL:    cfg.SessionTTL,
		MaxLoginFails: 5,
		LockoutFor:    15 * time.Minute,
		TOTPIssuer:    cfg.TOTPIssuer,
	})

	if err := bootstrapUser(ctx, authSvc, cfg); err != nil {
		return err
	}

	filesSvc := files.NewService(ws, auditLog, files.Options{
		MaxReadSize:    cfg.MaxReadSize,
		MaxWriteSize:   cfg.MaxWriteSize,
		MaxUploadSize:  cfg.MaxUploadSize,
		MaxListEntries: cfg.MaxListEntries,
	})

	termStore := terminal.NewStore(conn)
	termCfg := terminal.ManagerConfig{
		Shell:           cfg.Shell,
		Args:            cfg.ShellArgs,
		WorkingDir:      cfg.HomeDir,
		Env:             cfg.TerminalEnvironment(),
		MaxSessions:     cfg.TerminalMaxSessions,
		MaxPerUser:      cfg.TerminalMaxPerUser,
		IdleTimeout:     cfg.TerminalIdleTimeout,
		ScrollbackBytes: cfg.TerminalScrollback,
	}

	// SSH connection manager: config editing, key/host-key helpers and the
	// launcher that turns an alias into an ssh command line.
	var sshSvc *sshsvc.Service
	if cfg.SSHEnabled {
		askpassBin, err := os.Executable()
		if err != nil {
			slog.Warn("cannot resolve own executable; stored ssh passwords will be disabled", "error", err)
			askpassBin = ""
		}
		secrets, err := sshsecret.NewManager(conn, cfg.SessionSecret)
		if err != nil {
			return fmt.Errorf("initialize ssh secrets: %w", err)
		}
		broker, err := sshaskpass.New(slog.Default())
		if err != nil {
			return fmt.Errorf("initialize askpass broker: %w", err)
		}
		defer broker.Close()

		sshSvc, err = sshsvc.New(sshsvc.Config{
			Enabled:    true,
			Home:       cfg.HomeDir,
			ConfigPath: cfg.SSHConfigPath,
			SSHBin:     cfg.SSHBin,
			KeyscanBin: cfg.SSHKeyscanBin,
			KeygenBin:  cfg.SSHKeygenBin,
			AskpassBin: askpassBin,
			Log:        slog.Default(),
		}, conn, secrets, broker)
		if err != nil {
			return fmt.Errorf("initialize ssh service: %w", err)
		}
		termCfg.SSHCommand = func(alias string) (terminal.SSHLaunch, error) {
			argv, env, cleanup, err := sshSvc.Launch(ctx, alias)
			if err != nil {
				return terminal.SSHLaunch{}, err
			}
			return terminal.SSHLaunch{Argv: argv, Env: env, Cleanup: cleanup}, nil
		}
		slog.Info("ssh connection manager ready",
			"config", sshSvc.Config().ConfigPath, "home", cfg.HomeDir)
	}

	termMgr := terminal.NewManager(termCfg, termStore, auditLog)
	termMgr.MarkStaleSessions(ctx)

	// Background host-metrics sampler for the dashboard.
	monitor := sysinfo.New(cfg.MonitorInterval)
	monitor.Start(ctx)

	// Host operations: systemd services, processes, logs, package updates.
	opsSvc := sysops.New(sysops.Config{
		Enabled:       cfg.OpsEnabled,
		SystemctlBin:  cfg.OpsSystemctlBin,
		JournalctlBin: cfg.OpsJournalctlBin,
		LogDir:        cfg.OpsLogDir,
		ProcRoot:      cfg.OpsProcRoot,
	})
	if cfg.OpsEnabled {
		slog.Info("host operations ready",
			"systemctl", cfg.OpsSystemctlBin,
			"journalctl", cfg.OpsJournalctlBin,
			"logdir", cfg.OpsLogDir)
	}

	api := httpapi.New(cfg, authSvc, filesSvc, termMgr, auditLog, monitor, sshSvc, opsSvc)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}

	// Background maintenance: purge expired login sessions and old audit rows.
	maintCtx, maintCancel := context.WithCancel(ctx)
	defer maintCancel()
	go maintenance(maintCtx, authSvc, auditLog, cfg.AuditRetentionDays)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.Addr, "static", cfg.StaticDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	termMgr.Shutdown(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("server stopped")
	return nil
}

func bootstrapUser(ctx context.Context, svc *auth.Service, cfg *config.Config) error {
	generated, created, err := svc.EnsureBootstrapUser(ctx, cfg.BootstrapUser, cfg.BootstrapPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin user: %w", err)
	}
	if !created {
		return nil
	}
	if generated != "" {
		// Printed exactly once so the operator can log in. It is never stored
		// in plaintext anywhere.
		slog.Warn("no DASH_BOOTSTRAP_PASSWORD was set; a random password was generated",
			"username", cfg.BootstrapUser, "password", generated)
		fmt.Printf("\n========================================\n")
		fmt.Printf("  Initial admin credentials (shown once)\n")
		fmt.Printf("  username: %s\n", cfg.BootstrapUser)
		fmt.Printf("  password: %s\n", generated)
		fmt.Printf("  Change this password after logging in.\n")
		fmt.Printf("========================================\n\n")
	} else {
		slog.Info("bootstrap admin user created", "username", cfg.BootstrapUser)
	}
	return nil
}

func maintenance(ctx context.Context, authSvc *auth.Service, auditLog *audit.Logger, retentionDays int) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	prune := func() {
		authSvc.Cleanup(ctx)
		if retentionDays > 0 {
			cutoff := time.Now().AddDate(0, 0, -retentionDays)
			if n, err := auditLog.Prune(ctx, cutoff); err != nil {
				slog.Error("prune audit log failed", "error", err)
			} else if n > 0 {
				slog.Info("pruned old audit entries", "count", n, "olderThanDays", retentionDays)
			}
		}
	}
	prune()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func setupLogging(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(handler))
}
