package checker

import (
	"context"
	"errors"
	"runtime"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

type ICMPChecker struct {
	timeout time.Duration
}

func NewICMPChecker(timeout time.Duration) *ICMPChecker {
	return &ICMPChecker{timeout: timeout}
}

func (c *ICMPChecker) Check(ctx context.Context, ip string) CheckResult {
	result := CheckResult{
		IP:        ip,
		CheckedAt: time.Now(),
	}

	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}

	pinger, err := probing.NewPinger(ip)
	if err != nil {
		result.Err = err
		return result
	}

	pinger.Count = 1
	pinger.Timeout = c.timeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			result.Err = context.DeadlineExceeded
			return result
		}
		if remaining < pinger.Timeout {
			pinger.Timeout = remaining
		}
	}
	if runtime.GOOS == "windows" {
		pinger.SetPrivileged(true)
	}

	var latency time.Duration
	pinger.OnRecv = func(pkt *probing.Packet) {
		latency = pkt.Rtt
	}

	if err := pinger.Run(); err != nil {
		if ctx.Err() != nil {
			result.Err = ctx.Err()
			return result
		}
		result.Err = err
		return result
	}

	stats := pinger.Statistics()
	if stats.PacketsRecv > 0 {
		result.Healthy = true
		if latency > 0 {
			result.Latency = latency
		} else {
			result.Latency = stats.AvgRtt
		}
		return result
	}

	result.Err = errors.New("no reply received")
	return result
}
