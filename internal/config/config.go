package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	defaultRecordType       = "A"
	defaultTTL              = 60
	defaultIntervalSeconds  = 10
	defaultTimeoutSeconds   = 2
	defaultFailureThreshold = 3
	defaultRecovery         = 3
	defaultCooldownSeconds  = 30
	defaultMethod           = "icmp"
	defaultLogLevel         = "info"
)

type Config struct {
	Cloudflare CloudflareConfig `json:"cloudflare"`
	DNS        DNSConfig        `json:"dns"`
	Checks     ChecksConfig     `json:"checks"`
	Targets    []TargetConfig   `json:"targets"`
	Logging    LoggingConfig    `json:"logging"`
	Safety     SafetyConfig     `json:"safety"`
}

type CloudflareConfig struct {
	APIToken string `json:"api_token"`
	ZoneID   string `json:"zone_id"`
}

type DNSConfig struct {
	Name       string `json:"name"`
	RecordType string `json:"record_type"`
	TTL        int    `json:"ttl"`
	Proxied    bool   `json:"proxied"`
}

type ChecksConfig struct {
	IntervalSeconds   int    `json:"interval_seconds"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	FailureThreshold  int    `json:"failure_threshold"`
	RecoveryThreshold int    `json:"recovery_threshold"`
	CooldownSeconds   int    `json:"cooldown_seconds"`
	Method            string `json:"method"`
}

type TargetConfig struct {
	IP      string `json:"ip"`
	Enabled bool   `json:"enabled"`
}

type LoggingConfig struct {
	Level string `json:"level"`
}

type SafetyConfig struct {
	AllowZeroRecords bool `json:"allow_zero_records"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	var cfg Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.DNS.RecordType == "" {
		c.DNS.RecordType = defaultRecordType
	}
	if c.DNS.TTL == 0 {
		c.DNS.TTL = defaultTTL
	}
	if c.Checks.IntervalSeconds == 0 {
		c.Checks.IntervalSeconds = defaultIntervalSeconds
	}
	if c.Checks.TimeoutSeconds == 0 {
		c.Checks.TimeoutSeconds = defaultTimeoutSeconds
	}
	if c.Checks.FailureThreshold == 0 {
		c.Checks.FailureThreshold = defaultFailureThreshold
	}
	if c.Checks.RecoveryThreshold == 0 {
		c.Checks.RecoveryThreshold = defaultRecovery
	}
	if c.Checks.CooldownSeconds == 0 {
		c.Checks.CooldownSeconds = defaultCooldownSeconds
	}
	if c.Checks.Method == "" {
		c.Checks.Method = defaultMethod
	}
	if c.Logging.Level == "" {
		c.Logging.Level = defaultLogLevel
	}
}

func (c Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.Cloudflare.APIToken) == "" {
		errs = append(errs, errors.New("cloudflare.api_token is required"))
	}
	if strings.TrimSpace(c.DNS.Name) == "" {
		errs = append(errs, errors.New("dns.name is required"))
	}
	if !strings.EqualFold(c.DNS.RecordType, "A") {
		errs = append(errs, fmt.Errorf("dns.record_type must be A, got %q", c.DNS.RecordType))
	}
	if c.DNS.TTL <= 0 {
		errs = append(errs, errors.New("dns.ttl must be greater than 0"))
	}
	if !strings.EqualFold(c.Checks.Method, "icmp") {
		errs = append(errs, fmt.Errorf("checks.method must be icmp in v1, got %q", c.Checks.Method))
	}
	if c.Checks.IntervalSeconds <= 0 {
		errs = append(errs, errors.New("checks.interval_seconds must be greater than 0"))
	}
	if c.Checks.TimeoutSeconds <= 0 {
		errs = append(errs, errors.New("checks.timeout_seconds must be greater than 0"))
	}
	if c.Checks.TimeoutSeconds >= c.Checks.IntervalSeconds {
		errs = append(errs, errors.New("checks.timeout_seconds must be less than checks.interval_seconds"))
	}
	if c.Checks.FailureThreshold <= 0 {
		errs = append(errs, errors.New("checks.failure_threshold must be greater than 0"))
	}
	if c.Checks.RecoveryThreshold <= 0 {
		errs = append(errs, errors.New("checks.recovery_threshold must be greater than 0"))
	}
	if c.Checks.CooldownSeconds <= 0 {
		errs = append(errs, errors.New("checks.cooldown_seconds must be greater than 0"))
	}

	seen := map[string]struct{}{}
	enabledCount := 0

	for idx, target := range c.Targets {
		ip := strings.TrimSpace(target.IP)
		if ip == "" {
			errs = append(errs, fmt.Errorf("targets[%d].ip is required", idx))
			continue
		}

		addr, err := netip.ParseAddr(ip)
		if err != nil {
			errs = append(errs, fmt.Errorf("targets[%d].ip must be a valid IPv4 address: %w", idx, err))
			continue
		}
		if !addr.Is4() {
			errs = append(errs, fmt.Errorf("targets[%d].ip must be IPv4", idx))
		}
		if !isPublicIPv4(addr) {
			errs = append(errs, fmt.Errorf("targets[%d].ip must be a public IPv4 address", idx))
		}
		if _, ok := seen[ip]; ok {
			errs = append(errs, fmt.Errorf("duplicate target IP %q", ip))
		}
		seen[ip] = struct{}{}

		if target.Enabled {
			enabledCount++
		}
	}

	if enabledCount == 0 {
		errs = append(errs, errors.New("at least one enabled target is required"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (c Config) EnabledTargets() []TargetConfig {
	targets := make([]TargetConfig, 0, len(c.Targets))
	for _, target := range c.Targets {
		if target.Enabled {
			targets = append(targets, target)
		}
	}
	return targets
}

func (c Config) AllTargetIPs() []string {
	ips := make([]string, 0, len(c.Targets))
	for _, target := range c.Targets {
		ips = append(ips, target.IP)
	}
	slices.Sort(ips)
	return ips
}

func (c Config) Interval() time.Duration {
	return time.Duration(c.Checks.IntervalSeconds) * time.Second
}

func (c Config) Timeout() time.Duration {
	return time.Duration(c.Checks.TimeoutSeconds) * time.Second
}

func (c Config) Cooldown() time.Duration {
	return time.Duration(c.Checks.CooldownSeconds) * time.Second
}

func isPublicIPv4(addr netip.Addr) bool {
	return addr.Is4() &&
		!addr.IsPrivate() &&
		!addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() &&
		!addr.IsMulticast() &&
		!addr.IsInterfaceLocalMulticast() &&
		!addr.IsUnspecified()
}
