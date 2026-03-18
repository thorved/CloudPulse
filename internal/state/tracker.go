package state

import (
	"slices"
	"time"

	"github.com/thorved/CloudPulse/internal/checker"
)

type TargetState struct {
	IP                   string
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	DesiredInDNS         bool
	ActualInDNS          bool
	LastTransition       time.Time
	LastHealthy          time.Time
	LastError            string
}

type Tracker struct {
	order  []string
	states map[string]*TargetState
}

func NewTracker(targetIPs []string) *Tracker {
	order := slices.Clone(targetIPs)
	slices.Sort(order)

	states := make(map[string]*TargetState, len(order))
	for _, ip := range order {
		states[ip] = &TargetState{IP: ip}
	}

	return &Tracker{
		order:  order,
		states: states,
	}
}

func (t *Tracker) InitializeFromActual(actual map[string]bool) {
	for _, ip := range t.order {
		state := t.states[ip]
		active := actual[ip]
		state.ActualInDNS = active
		state.DesiredInDNS = active
	}
}

func (t *Tracker) SyncActual(actual map[string]bool) {
	for _, ip := range t.order {
		t.states[ip].ActualInDNS = actual[ip]
	}
}

func (t *Tracker) ApplyCheck(result checker.CheckResult, failureThreshold, recoveryThreshold int, cooldown time.Duration, now time.Time) {
	state, ok := t.states[result.IP]
	if !ok {
		return
	}

	if result.Healthy {
		state.ConsecutiveSuccesses++
		state.ConsecutiveFailures = 0
		state.LastHealthy = now
		state.LastError = ""
	} else {
		state.ConsecutiveFailures++
		state.ConsecutiveSuccesses = 0
		if result.Err != nil {
			state.LastError = result.Err.Error()
		} else {
			state.LastError = "unhealthy"
		}
	}

	if state.ActualInDNS {
		state.DesiredInDNS = true
		if state.ConsecutiveFailures >= failureThreshold && cooldownElapsed(state.LastTransition, now, cooldown) {
			state.DesiredInDNS = false
		}
		return
	}

	state.DesiredInDNS = false
	if state.ConsecutiveSuccesses >= recoveryThreshold && cooldownElapsed(state.LastTransition, now, cooldown) {
		state.DesiredInDNS = true
	}
}

func (t *Tracker) ForceDesired(ip string, desired bool) {
	state, ok := t.states[ip]
	if !ok {
		return
	}
	state.DesiredInDNS = desired
}

func (t *Tracker) MarkTransition(ip string, active bool, at time.Time) {
	state, ok := t.states[ip]
	if !ok {
		return
	}
	state.ActualInDNS = active
	state.DesiredInDNS = active
	state.LastTransition = at
}

func (t *Tracker) State(ip string) (TargetState, bool) {
	state, ok := t.states[ip]
	if !ok {
		return TargetState{}, false
	}
	return *state, true
}

func (t *Tracker) States() []TargetState {
	states := make([]TargetState, 0, len(t.order))
	for _, ip := range t.order {
		states = append(states, *t.states[ip])
	}
	return states
}

func cooldownElapsed(lastTransition, now time.Time, cooldown time.Duration) bool {
	if lastTransition.IsZero() {
		return true
	}
	return now.Sub(lastTransition) >= cooldown
}
