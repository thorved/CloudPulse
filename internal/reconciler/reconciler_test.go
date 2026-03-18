package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/thorved/CloudPulse/internal/checker"
	"github.com/thorved/CloudPulse/internal/cloudflare"
	"github.com/thorved/CloudPulse/internal/config"
	"github.com/thorved/CloudPulse/internal/state"
)

func TestReconcilerPreservesOneRecordWhenAllTargetsFail(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{
		records: []cloudflare.DNSRecord{
			{ID: "one", Name: "app.example.com", Type: "A", Content: "1.2.3.4", TTL: 60},
			{ID: "two", Name: "app.example.com", Type: "A", Content: "5.6.7.8", TTL: 60},
		},
	}
	checker := newFakeChecker(map[string][]checker.CheckResult{
		"1.2.3.4": {
			{IP: "1.2.3.4", Err: errors.New("timeout")},
			{IP: "1.2.3.4", Err: errors.New("timeout")},
			{IP: "1.2.3.4", Err: errors.New("timeout")},
		},
		"5.6.7.8": {
			{IP: "5.6.7.8", Err: errors.New("timeout")},
			{IP: "5.6.7.8", Err: errors.New("timeout")},
			{IP: "5.6.7.8", Err: errors.New("timeout")},
		},
	})

	service := newTestReconciler(t, testConfig(false, 3, 3), provider, checker)
	if err := service.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := service.RunCycle(context.Background()); err != nil {
			t.Fatalf("run cycle %d: %v", i, err)
		}
	}

	records, err := provider.ListRecords(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("list records: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected one record to remain, got %d", len(records))
	}
	if records[0].Content != "1.2.3.4" {
		t.Fatalf("expected lexically first record to remain, got %q", records[0].Content)
	}
}

func TestReconcilerAllowsZeroRecordsWhenConfigured(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{
		records: []cloudflare.DNSRecord{
			{ID: "one", Name: "app.example.com", Type: "A", Content: "1.2.3.4", TTL: 60},
			{ID: "two", Name: "app.example.com", Type: "A", Content: "5.6.7.8", TTL: 60},
		},
	}
	checker := newFakeChecker(map[string][]checker.CheckResult{
		"1.2.3.4": {
			{IP: "1.2.3.4", Err: errors.New("timeout")},
			{IP: "1.2.3.4", Err: errors.New("timeout")},
			{IP: "1.2.3.4", Err: errors.New("timeout")},
		},
		"5.6.7.8": {
			{IP: "5.6.7.8", Err: errors.New("timeout")},
			{IP: "5.6.7.8", Err: errors.New("timeout")},
			{IP: "5.6.7.8", Err: errors.New("timeout")},
		},
	})

	service := newTestReconciler(t, testConfig(true, 3, 3), provider, checker)
	if err := service.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := service.RunCycle(context.Background()); err != nil {
			t.Fatalf("run cycle %d: %v", i, err)
		}
	}

	records, err := provider.ListRecords(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected zero records to remain, got %d", len(records))
	}
}

func TestReconcilerFixesDriftAndRemovesDuplicates(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{
		records: []cloudflare.DNSRecord{
			{ID: "keep-me", Name: "app.example.com", Type: "A", Content: "1.2.3.4", TTL: 120, Proxied: true},
			{ID: "duplicate", Name: "app.example.com", Type: "A", Content: "1.2.3.4", TTL: 60, Proxied: false},
			{ID: "disabled", Name: "app.example.com", Type: "A", Content: "3.4.5.6", TTL: 60, Proxied: false},
			{ID: "unexpected", Name: "app.example.com", Type: "A", Content: "9.9.9.9", TTL: 60, Proxied: false},
			{ID: "ignore", Name: "app.example.com", Type: "CNAME", Content: "elsewhere.example.com", TTL: 60, Proxied: false},
		},
	}
	checker := newFakeChecker(map[string][]checker.CheckResult{
		"1.2.3.4": {{IP: "1.2.3.4", Healthy: true}},
		"5.6.7.8": {{IP: "5.6.7.8", Healthy: true}},
	})

	cfg := testConfig(false, 1, 1)
	cfg.Targets = []config.TargetConfig{
		{IP: "1.2.3.4", Enabled: true},
		{IP: "5.6.7.8", Enabled: true},
		{IP: "3.4.5.6", Enabled: false},
	}

	service := newTestReconciler(t, cfg, provider, checker)
	if err := service.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := service.RunCycle(context.Background()); err != nil {
		t.Fatalf("run cycle: %v", err)
	}

	records, err := provider.ListRecords(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	slices.SortFunc(records, func(a, b cloudflare.DNSRecord) int {
		return compareStrings(a.Content, b.Content)
	})

	if len(records) != 3 {
		t.Fatalf("expected 3 records after reconcile, got %d", len(records))
	}
	if records[0].Type != "A" || records[0].Content != "1.2.3.4" || records[0].TTL != 60 || records[0].Proxied {
		t.Fatalf("expected corrected record for 1.2.3.4, got %+v", records[0])
	}
	if records[1].Type != "A" || records[1].Content != "5.6.7.8" {
		t.Fatalf("expected created A record for 5.6.7.8, got %+v", records[1])
	}
	if records[2].Type != "CNAME" {
		t.Fatalf("expected non-A record to remain untouched, got %+v", records[2])
	}
}

type fakeProvider struct {
	mu      sync.Mutex
	records []cloudflare.DNSRecord
	nextID  int
}

func (f *fakeProvider) ListRecords(_ context.Context, name string) ([]cloudflare.DNSRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	records := make([]cloudflare.DNSRecord, 0, len(f.records))
	for _, record := range f.records {
		if record.Name == name {
			records = append(records, record)
		}
	}
	return records, nil
}

func (f *fakeProvider) CreateARecord(_ context.Context, name, ip string, ttl int, proxied bool) (cloudflare.DNSRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextID++
	record := cloudflare.DNSRecord{
		ID:      idOrGenerated(f.nextID),
		Name:    name,
		Type:    "A",
		Content: ip,
		TTL:     ttl,
		Proxied: proxied,
	}
	f.records = append(f.records, record)
	return record, nil
}

func (f *fakeProvider) UpdateRecord(_ context.Context, recordID string, ttl int, proxied bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for idx := range f.records {
		if f.records[idx].ID == recordID {
			f.records[idx].TTL = ttl
			f.records[idx].Proxied = proxied
			return nil
		}
	}
	return errors.New("record not found")
}

func (f *fakeProvider) DeleteRecord(_ context.Context, recordID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for idx := range f.records {
		if f.records[idx].ID == recordID {
			f.records = append(f.records[:idx], f.records[idx+1:]...)
			return nil
		}
	}
	return errors.New("record not found")
}

type fakeChecker struct {
	mu      sync.Mutex
	results map[string][]checker.CheckResult
}

func newFakeChecker(results map[string][]checker.CheckResult) *fakeChecker {
	return &fakeChecker{results: results}
}

func (f *fakeChecker) Check(_ context.Context, ip string) checker.CheckResult {
	f.mu.Lock()
	defer f.mu.Unlock()

	queue := f.results[ip]
	if len(queue) == 0 {
		return checker.CheckResult{IP: ip, Healthy: true}
	}

	result := queue[0]
	f.results[ip] = queue[1:]
	result.IP = ip
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now()
	}
	return result
}

func newTestReconciler(t *testing.T, cfg config.Config, provider cloudflare.DNSProvider, checker checker.HealthChecker) *Reconciler {
	t.Helper()

	testLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, provider, checker, state.NewTracker(cfg.AllTargetIPs()), testLogger, false)
}

func testConfig(allowZero bool, failureThreshold, recoveryThreshold int) config.Config {
	return config.Config{
		DNS: config.DNSConfig{
			Name:       "app.example.com",
			RecordType: "A",
			TTL:        60,
			Proxied:    false,
		},
		Checks: config.ChecksConfig{
			IntervalSeconds:   10,
			TimeoutSeconds:    2,
			FailureThreshold:  failureThreshold,
			RecoveryThreshold: recoveryThreshold,
			CooldownSeconds:   1,
			Method:            "icmp",
		},
		Targets: []config.TargetConfig{
			{IP: "1.2.3.4", Enabled: true},
			{IP: "5.6.7.8", Enabled: true},
		},
		Safety: config.SafetyConfig{
			AllowZeroRecords: allowZero,
		},
	}
}

func idOrGenerated(nextID int) string {
	return "generated-" + time.Unix(int64(nextID), 0).UTC().Format("150405")
}

func compareStrings(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
