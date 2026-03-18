package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thorved/CloudPulse/internal/checker"
	"github.com/thorved/CloudPulse/internal/cloudflare"
	"github.com/thorved/CloudPulse/internal/config"
	"github.com/thorved/CloudPulse/internal/state"
)

type ActionKind string

const (
	ActionCreate ActionKind = "create"
	ActionUpdate ActionKind = "update"
	ActionDelete ActionKind = "delete"
)

type Action struct {
	Kind            ActionKind
	IP              string
	RecordID        string
	Reason          string
	TrackTransition bool
	ResultingActive bool
}

type CycleReport struct {
	Results []checker.CheckResult
	Actions []Action
}

type Reconciler struct {
	cfg      config.Config
	provider cloudflare.DNSProvider
	checker  checker.HealthChecker
	tracker  *state.Tracker
	logger   *slog.Logger
	dryRun   bool
}

func New(cfg config.Config, provider cloudflare.DNSProvider, checker checker.HealthChecker, tracker *state.Tracker, logger *slog.Logger, dryRun bool) *Reconciler {
	return &Reconciler{
		cfg:      cfg,
		provider: provider,
		checker:  checker,
		tracker:  tracker,
		logger:   logger,
		dryRun:   dryRun,
	}
}

func (r *Reconciler) Initialize(ctx context.Context) error {
	records, err := r.provider.ListRecords(ctx, r.cfg.DNS.Name)
	if err != nil {
		return fmt.Errorf("initial cloudflare sync failed: %w", err)
	}

	actual, _, _ := r.classifyRecords(records)
	r.tracker.InitializeFromActual(actual)

	r.logger.Info("startup sync complete",
		"hostname", r.cfg.DNS.Name,
		"managed_targets", len(r.cfg.Targets),
		"active_records", countActive(actual),
	)

	return nil
}

func (r *Reconciler) RunCycle(ctx context.Context) (CycleReport, error) {
	results, err := r.runChecks(ctx)
	if err != nil {
		return CycleReport{}, err
	}

	now := time.Now()
	for _, result := range results {
		r.logResult(result)
		r.tracker.ApplyCheck(result, r.cfg.Checks.FailureThreshold, r.cfg.Checks.RecoveryThreshold, r.cfg.Cooldown(), now)
	}

	targetsByIP := make(map[string]config.TargetConfig, len(r.cfg.Targets))
	for _, target := range r.cfg.Targets {
		targetsByIP[target.IP] = target
		if !target.Enabled {
			r.tracker.ForceDesired(target.IP, false)
		}
	}

	records, err := r.provider.ListRecords(ctx, r.cfg.DNS.Name)
	if err != nil {
		return CycleReport{Results: results}, fmt.Errorf("list cloudflare records: %w", err)
	}

	actual, recordsByIP, unexpected := r.classifyRecords(records)
	r.tracker.SyncActual(actual)
	r.enforceSafety(targetsByIP)

	actions := r.planActions(targetsByIP, recordsByIP, unexpected)
	report := CycleReport{
		Results: results,
		Actions: actions,
	}

	if len(actions) == 0 {
		return report, nil
	}

	if err := r.applyActions(ctx, actions); err != nil {
		return report, err
	}

	return report, nil
}

func (r *Reconciler) runChecks(ctx context.Context) ([]checker.CheckResult, error) {
	targets := r.cfg.EnabledTargets()
	results := make([]checker.CheckResult, 0, len(targets))
	if len(targets) == 0 {
		return results, nil
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		cancelErr error
	)

	wg.Add(len(targets))
	for _, target := range targets {
		ip := target.IP
		go func() {
			defer wg.Done()
			result := r.checker.Check(ctx, ip)

			mu.Lock()
			defer mu.Unlock()
			results = append(results, result)
			if result.Err != nil && ctx.Err() != nil && cancelErr == nil {
				cancelErr = ctx.Err()
			}
		}()
	}

	wg.Wait()
	slices.SortFunc(results, func(a, b checker.CheckResult) int {
		return strings.Compare(a.IP, b.IP)
	})

	if cancelErr != nil {
		return nil, cancelErr
	}

	return results, nil
}

func (r *Reconciler) logResult(result checker.CheckResult) {
	if result.Healthy {
		r.logger.Info("target check",
			"ip", result.IP,
			"status", "healthy",
			"latency_ms", result.Latency.Milliseconds(),
		)
		return
	}

	r.logger.Warn("target check",
		"ip", result.IP,
		"status", "unhealthy",
		"err", errorString(result.Err),
	)
}

func (r *Reconciler) classifyRecords(records []cloudflare.DNSRecord) (map[string]bool, map[string][]cloudflare.DNSRecord, []cloudflare.DNSRecord) {
	configured := make(map[string]struct{}, len(r.cfg.Targets))
	for _, target := range r.cfg.Targets {
		configured[target.IP] = struct{}{}
	}

	actual := make(map[string]bool, len(r.cfg.Targets))
	recordsByIP := make(map[string][]cloudflare.DNSRecord, len(r.cfg.Targets))
	unexpected := make([]cloudflare.DNSRecord, 0)

	for _, record := range records {
		if !strings.EqualFold(record.Name, r.cfg.DNS.Name) {
			continue
		}
		if !strings.EqualFold(record.Type, "A") {
			continue
		}

		if _, ok := configured[record.Content]; ok {
			actual[record.Content] = true
			recordsByIP[record.Content] = append(recordsByIP[record.Content], record)
			continue
		}

		unexpected = append(unexpected, record)
	}

	for ip := range recordsByIP {
		slices.SortFunc(recordsByIP[ip], func(a, b cloudflare.DNSRecord) int {
			return strings.Compare(a.ID, b.ID)
		})
	}
	slices.SortFunc(unexpected, func(a, b cloudflare.DNSRecord) int {
		return strings.Compare(a.ID, b.ID)
	})

	return actual, recordsByIP, unexpected
}

func (r *Reconciler) enforceSafety(targetsByIP map[string]config.TargetConfig) {
	if r.cfg.Safety.AllowZeroRecords {
		return
	}

	anyDesired := false
	candidates := make([]state.TargetState, 0)

	for ip, target := range targetsByIP {
		if !target.Enabled {
			continue
		}

		targetState, ok := r.tracker.State(ip)
		if !ok {
			continue
		}
		if targetState.DesiredInDNS {
			anyDesired = true
			break
		}
		if targetState.ActualInDNS {
			candidates = append(candidates, targetState)
		}
	}

	if anyDesired || len(candidates) == 0 {
		return
	}

	slices.SortFunc(candidates, func(a, b state.TargetState) int {
		switch {
		case a.LastHealthy.After(b.LastHealthy):
			return -1
		case a.LastHealthy.Before(b.LastHealthy):
			return 1
		default:
			return strings.Compare(a.IP, b.IP)
		}
	})

	r.tracker.ForceDesired(candidates[0].IP, true)
}

func (r *Reconciler) planActions(targetsByIP map[string]config.TargetConfig, recordsByIP map[string][]cloudflare.DNSRecord, unexpected []cloudflare.DNSRecord) []Action {
	actions := make([]Action, 0)
	ips := make([]string, 0, len(targetsByIP))
	for ip := range targetsByIP {
		ips = append(ips, ip)
	}
	slices.Sort(ips)

	for _, ip := range ips {
		target := targetsByIP[ip]
		targetState, _ := r.tracker.State(ip)
		desired := target.Enabled && targetState.DesiredInDNS
		records := recordsByIP[ip]

		if desired {
			if len(records) == 0 {
				actions = append(actions, Action{
					Kind:            ActionCreate,
					IP:              ip,
					Reason:          r.reasonForEnable(target, targetState),
					TrackTransition: true,
					ResultingActive: true,
				})
				continue
			}

			if records[0].TTL != r.cfg.DNS.TTL || records[0].Proxied != r.cfg.DNS.Proxied {
				actions = append(actions, Action{
					Kind:     ActionUpdate,
					IP:       ip,
					RecordID: records[0].ID,
					Reason:   "ttl/proxied drift",
				})
			}

			for _, duplicate := range records[1:] {
				actions = append(actions, Action{
					Kind:     ActionDelete,
					IP:       ip,
					RecordID: duplicate.ID,
					Reason:   "duplicate record",
				})
			}

			continue
		}

		for idx, record := range records {
			actions = append(actions, Action{
				Kind:            ActionDelete,
				IP:              ip,
				RecordID:        record.ID,
				Reason:          r.reasonForDisable(target, targetState),
				TrackTransition: idx == len(records)-1,
				ResultingActive: false,
			})
		}
	}

	for _, record := range unexpected {
		actions = append(actions, Action{
			Kind:     ActionDelete,
			IP:       record.Content,
			RecordID: record.ID,
			Reason:   "unexpected record for managed hostname",
		})
	}

	return actions
}

func (r *Reconciler) applyActions(ctx context.Context, actions []Action) error {
	for _, action := range actions {
		if r.dryRun {
			r.logger.Info("dns action (dry-run)",
				"kind", action.Kind,
				"ip", action.IP,
				"hostname", r.cfg.DNS.Name,
				"reason", action.Reason,
			)
			continue
		}

		switch action.Kind {
		case ActionCreate:
			if _, err := r.provider.CreateARecord(ctx, r.cfg.DNS.Name, action.IP, r.cfg.DNS.TTL, r.cfg.DNS.Proxied); err != nil {
				return fmt.Errorf("create DNS record for %s: %w", action.IP, err)
			}
		case ActionUpdate:
			if err := r.provider.UpdateRecord(ctx, action.RecordID, r.cfg.DNS.TTL, r.cfg.DNS.Proxied); err != nil {
				return fmt.Errorf("update DNS record %s for %s: %w", action.RecordID, action.IP, err)
			}
		case ActionDelete:
			if err := r.provider.DeleteRecord(ctx, action.RecordID); err != nil {
				return fmt.Errorf("delete DNS record %s for %s: %w", action.RecordID, action.IP, err)
			}
		default:
			return fmt.Errorf("unsupported action kind %q", action.Kind)
		}

		if action.TrackTransition {
			r.tracker.MarkTransition(action.IP, action.ResultingActive, time.Now())
		}

		r.logger.Info("dns action",
			"kind", action.Kind,
			"ip", action.IP,
			"hostname", r.cfg.DNS.Name,
			"reason", action.Reason,
		)
	}

	return nil
}

func (r *Reconciler) reasonForEnable(target config.TargetConfig, targetState state.TargetState) string {
	if targetState.ConsecutiveSuccesses >= r.cfg.Checks.RecoveryThreshold {
		return fmt.Sprintf("%d consecutive successes", r.cfg.Checks.RecoveryThreshold)
	}
	if !targetState.ActualInDNS {
		return "manual drift correction"
	}
	return "desired healthy set"
}

func (r *Reconciler) reasonForDisable(target config.TargetConfig, targetState state.TargetState) string {
	if !target.Enabled {
		return "target disabled"
	}
	if targetState.ConsecutiveFailures >= r.cfg.Checks.FailureThreshold {
		return fmt.Sprintf("%d consecutive failures", r.cfg.Checks.FailureThreshold)
	}
	return "desired healthy set"
}

func countActive(actual map[string]bool) int {
	count := 0
	for _, active := range actual {
		if active {
			count++
		}
	}
	return count
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
