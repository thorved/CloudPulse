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

	"github.com/thorved/CloudPulse/internal/checker"
	"github.com/thorved/CloudPulse/internal/cloudflare"
	"github.com/thorved/CloudPulse/internal/config"
	"github.com/thorved/CloudPulse/internal/logger"
	"github.com/thorved/CloudPulse/internal/reconciler"
	"github.com/thorved/CloudPulse/internal/state"
)

func main() {
	os.Exit(run(os.Args))
}

func run(args []string) int {
	if len(args) < 2 {
		printUsage()
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := args[1]
	switch command {
	case "run", "dry-run", "once", "validate":
		if err := executeCommand(ctx, command, args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "cloudpulse %s failed: %v\n", command, err)
			return 1
		}
		return 0
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", command)
		printUsage()
		return 2
	}
}

func executeCommand(ctx context.Context, command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config.json", "Path to the CloudPulse config file")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	appLogger, err := logger.New(cfg.Logging.Level)
	if err != nil {
		return err
	}

	provider := cloudflare.NewClient(cfg.Cloudflare.APIToken, cfg.Cloudflare.ZoneID, &http.Client{
		Timeout: 10 * time.Second,
	})

	if command == "validate" {
		return validateConfig(ctx, cfg, provider, appLogger)
	}

	healthChecker := checker.NewICMPChecker(cfg.Timeout())
	tracker := state.NewTracker(cfg.AllTargetIPs())
	service := reconciler.New(cfg, provider, healthChecker, tracker, appLogger, command == "dry-run")

	if err := service.Initialize(ctx); err != nil {
		return err
	}

	if command == "once" {
		_, err := service.RunCycle(ctx)
		return err
	}

	appLogger.Info("service starting",
		"mode", command,
		"hostname", cfg.DNS.Name,
		"interval_seconds", cfg.Checks.IntervalSeconds,
	)

	if _, err := service.RunCycle(ctx); err != nil {
		appLogger.Error("cycle failed", "err", err)
	}

	ticker := time.NewTicker(cfg.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				appLogger.Info("service stopped")
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			if _, err := service.RunCycle(ctx); err != nil && !errors.Is(err, context.Canceled) {
				appLogger.Error("cycle failed", "err", err)
			}
		}
	}
}

func validateConfig(ctx context.Context, cfg config.Config, provider cloudflare.DNSProvider, logger *slog.Logger) error {
	records, err := provider.ListRecords(ctx, cfg.DNS.Name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "authentication error") {
			if strings.TrimSpace(cfg.Cloudflare.ZoneID) != "" {
				return fmt.Errorf("cloudflare validation failed: %w; verify the API token has Zone DNS Read/Edit access and that cloudflare.zone_id=%q matches the zone for %s", err, cfg.Cloudflare.ZoneID, cfg.DNS.Name)
			}
			return fmt.Errorf("cloudflare validation failed: %w; verify the API token has Zone DNS Read/Edit access to the Cloudflare zone for %s", err, cfg.DNS.Name)
		}
		return fmt.Errorf("cloudflare validation failed: %w", err)
	}

	logger.Info("config validation succeeded",
		"hostname", cfg.DNS.Name,
		"records_found", len(records),
		"enabled_targets", len(cfg.EnabledTargets()),
	)

	return nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `CloudPulse keeps Cloudflare A records aligned with healthy targets.

Usage:
  cloudpulse <command> [-config path]

Commands:
  run        Run continuously and apply DNS changes
  dry-run    Run continuously without mutating Cloudflare
  once       Execute one health-check and reconciliation cycle
  validate   Validate config and Cloudflare access
`)
}
