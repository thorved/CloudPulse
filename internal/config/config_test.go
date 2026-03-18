package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg := writeAndLoadConfig(t, `{
		"cloudflare": {
			"api_token": "token"
		},
		"dns": {
			"name": "app.example.com"
		},
		"checks": {},
		"targets": [
			{"ip": "1.2.3.4", "enabled": true}
		]
	}`)

	if cfg.DNS.RecordType != "A" {
		t.Fatalf("expected default record type A, got %q", cfg.DNS.RecordType)
	}
	if cfg.DNS.TTL != 60 {
		t.Fatalf("expected default TTL 60, got %d", cfg.DNS.TTL)
	}
	if cfg.Checks.IntervalSeconds != 10 {
		t.Fatalf("expected default interval 10, got %d", cfg.Checks.IntervalSeconds)
	}
	if cfg.Checks.TimeoutSeconds != 2 {
		t.Fatalf("expected default timeout 2, got %d", cfg.Checks.TimeoutSeconds)
	}
	if cfg.Checks.FailureThreshold != 3 {
		t.Fatalf("expected default failure threshold 3, got %d", cfg.Checks.FailureThreshold)
	}
	if cfg.Checks.RecoveryThreshold != 3 {
		t.Fatalf("expected default recovery threshold 3, got %d", cfg.Checks.RecoveryThreshold)
	}
	if cfg.Checks.CooldownSeconds != 30 {
		t.Fatalf("expected default cooldown 30, got %d", cfg.Checks.CooldownSeconds)
	}
	if cfg.Checks.Method != "icmp" {
		t.Fatalf("expected default method icmp, got %q", cfg.Checks.Method)
	}
	if cfg.Logging.Level != "info" {
		t.Fatalf("expected default log level info, got %q", cfg.Logging.Level)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{
		"cloudflare": {
			"api_token": ""
		},
		"dns": {
			"name": "",
			"record_type": "AAAA",
			"ttl": -1
		},
		"checks": {
			"interval_seconds": 2,
			"timeout_seconds": 2,
			"failure_threshold": -1,
			"recovery_threshold": -1,
			"cooldown_seconds": -1,
			"method": "tcp"
		},
		"targets": [
			{"ip": "10.0.0.1", "enabled": true},
			{"ip": "10.0.0.1", "enabled": false}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error")
	}

	wantSubstrings := []string{
		"cloudflare.api_token is required",
		"dns.name is required",
		`dns.record_type must be A, got "AAAA"`,
		"dns.ttl must be greater than 0",
		"checks.method must be icmp",
		"checks.timeout_seconds must be less than checks.interval_seconds",
		"checks.failure_threshold must be greater than 0",
		"checks.recovery_threshold must be greater than 0",
		"checks.cooldown_seconds must be greater than 0",
		"targets[0].ip must be a public IPv4 address",
		"targets[1].ip must be a public IPv4 address",
		`duplicate target IP "10.0.0.1"`,
	}

	for _, want := range wantSubstrings {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
}

func writeAndLoadConfig(t *testing.T, contents string) Config {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	return cfg
}
