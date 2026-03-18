package state

import (
	"errors"
	"testing"
	"time"

	"github.com/thorved/CloudPulse/internal/checker"
)

func TestTrackerRemovesOnlyAfterFailureThreshold(t *testing.T) {
	t.Parallel()

	tracker := NewTracker([]string{"1.2.3.4"})
	tracker.InitializeFromActual(map[string]bool{"1.2.3.4": true})

	now := time.Now()
	for i := 0; i < 2; i++ {
		tracker.ApplyCheck(checker.CheckResult{IP: "1.2.3.4", Err: errors.New("timeout")}, 3, 3, time.Second, now)
		state, _ := tracker.State("1.2.3.4")
		if !state.DesiredInDNS {
			t.Fatalf("target should remain active before threshold, iteration %d", i)
		}
	}

	tracker.ApplyCheck(checker.CheckResult{IP: "1.2.3.4", Err: errors.New("timeout")}, 3, 3, time.Second, now)
	state, _ := tracker.State("1.2.3.4")
	if state.DesiredInDNS {
		t.Fatal("target should be inactive after reaching failure threshold")
	}
}

func TestTrackerRecoversOnlyAfterRecoveryThreshold(t *testing.T) {
	t.Parallel()

	tracker := NewTracker([]string{"1.2.3.4"})
	tracker.InitializeFromActual(map[string]bool{"1.2.3.4": false})

	now := time.Now()
	for i := 0; i < 2; i++ {
		tracker.ApplyCheck(checker.CheckResult{IP: "1.2.3.4", Healthy: true}, 3, 3, time.Second, now)
		state, _ := tracker.State("1.2.3.4")
		if state.DesiredInDNS {
			t.Fatalf("target should remain inactive before threshold, iteration %d", i)
		}
	}

	tracker.ApplyCheck(checker.CheckResult{IP: "1.2.3.4", Healthy: true}, 3, 3, time.Second, now)
	state, _ := tracker.State("1.2.3.4")
	if !state.DesiredInDNS {
		t.Fatal("target should be active after reaching recovery threshold")
	}
}

func TestTrackerCooldownBlocksImmediateRecovery(t *testing.T) {
	t.Parallel()

	tracker := NewTracker([]string{"1.2.3.4"})
	tracker.InitializeFromActual(map[string]bool{"1.2.3.4": false})

	now := time.Now()
	tracker.MarkTransition("1.2.3.4", false, now)

	for i := 0; i < 3; i++ {
		tracker.ApplyCheck(checker.CheckResult{IP: "1.2.3.4", Healthy: true}, 3, 3, 30*time.Second, now)
	}

	state, _ := tracker.State("1.2.3.4")
	if state.DesiredInDNS {
		t.Fatal("target should stay inactive while cooldown is active")
	}
}
