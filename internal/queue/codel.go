// Package queue provides Layer 7 admission control, priority queuing,
// and CoDel active queue management for Aegora.
package queue

import (
	"sync"
	"time"
)

// Codel implements a simplified Controlled Delay (CoDel) active queue management
// algorithm for Layer 7 gateway admission control.
//
// Simplifications vs. RFC 8289 (Full CoDel state machine):
//  1. Instantaneous sojourn evaluation: RFC 8289 tracks the minimum packet delay
//     over a sliding interval window to distinguish standing queues from transient bursts.
//     This implementation inspects instantaneous sojourn time per request at dequeue.
//  2. State entry: Rather than waiting a full Interval of sustained high delay before
//     dropping, this implementation enters dropping mode when consecutive dequeue checks
//     within Interval both observe sojourn times exceeding Target.
//  3. Pacing schedule: RFC 8289 drops packets along an inverse-square-root schedule
//     (Interval / sqrt(count)) to throttle TCP sender congestion windows gradually.
//     Here, while dropping is active, every request exceeding Target is dropped immediately
//     to shed excess gateway load without calculating dynamic pacing.
//  4. Recovery: RFC 8289 exits dropping mode when the window minimum drops below Target;
//     this implementation exits dropping mode immediately as soon as a request's sojourn
//     falls to or below Target.
type Codel struct {
	// Target is the acceptable queue latency limit (e.g. 100ms).
	Target time.Duration
	// Interval is the sliding time window used to judge persistence (e.g. 1s).
	Interval time.Duration

	mu       sync.Mutex
	lastDrop time.Time
	dropping bool
}

// NewCodel constructs a Codel controller with the given Target and Interval.
// It panics if target or interval are non-positive.
func NewCodel(target, interval time.Duration) *Codel {
	if target <= 0 {
		panic("queue: codel target must be positive")
	}
	if interval <= 0 {
		panic("queue: codel interval must be positive")
	}

	return &Codel{
		Target:   target,
		Interval: interval,
	}
}

// ShouldDrop reports whether a request should be dropped based on its sojourn time.
// If sojourn <= Target, dropping state is cleared and false is returned.
// If sojourn > Target persistently (meaning a previous check within Interval also
// observed sojourn > Target), drop state is recorded and true is returned.
func (c *Codel) ShouldDrop(sojourn time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	// If sojourn is at or below target, the queue is draining properly.
	if sojourn <= c.Target {
		c.dropping = false
		c.lastDrop = time.Time{}
		return false
	}

	// If already in dropping state and sojourn remains above target, continue dropping.
	if c.dropping {
		c.lastDrop = now
		return true
	}

	// If a previous check within Interval also saw sojourn > Target, this is persistent
	// bufferbloat: enter dropping mode and drop this request.
	if !c.lastDrop.IsZero() && now.Sub(c.lastDrop) <= c.Interval {
		c.dropping = true
		c.lastDrop = now
		return true
	}

	// First time observing sojourn > Target, or previous check occurred more than Interval ago.
	c.lastDrop = now
	return false
}

// Dropping reports whether the controller is currently in dropping mode.
func (c *Codel) Dropping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropping
}
