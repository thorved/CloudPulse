package checker

import (
	"context"
	"time"
)

type HealthChecker interface {
	Check(ctx context.Context, ip string) CheckResult
}

type CheckResult struct {
	IP        string
	Healthy   bool
	Latency   time.Duration
	Err       error
	CheckedAt time.Time
}
