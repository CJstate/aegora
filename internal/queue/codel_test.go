package queue

import (
	"testing"
	"time"
)

func TestNewCodel_PanicsOnNonPositive(t *testing.T) {
	tests := []struct {
		name     string
		target   time.Duration
		interval time.Duration
	}{
		{"zero target", 0, time.Second},
		{"negative target", -time.Millisecond, time.Second},
		{"zero interval", 100 * time.Millisecond, 0},
		{"negative interval", 100 * time.Millisecond, -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected NewCodel to panic for %s", tt.name)
				}
			}()
			_ = NewCodel(tt.target, tt.interval)
		})
	}
}

func TestCodel_TableDrivenShouldDrop(t *testing.T) {
	// Table-driven test verifying:
	// 1. Under target never drops
	// 2. Sustained over-target drops
	// 3. Recovers when sojourn falls back under target
	// 4. Over-target after long idle gap (> Interval) does not drop on first observation

	const target = 20 * time.Millisecond
	const interval = 100 * time.Millisecond

	steps := []struct {
		name      string
		sojourn   time.Duration
		sleep     time.Duration
		wantDrop  bool
		wantState bool // expected dropping state after check
	}{
		{
			name:      "under target 5ms -> no drop",
			sojourn:   5 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "under target 15ms -> no drop",
			sojourn:   15 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "first time over target 30ms -> no drop (not yet sustained)",
			sojourn:   30 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "second check over target 30ms within interval -> drops (sustained)",
			sojourn:   30 * time.Millisecond,
			sleep:     5 * time.Millisecond,
			wantDrop:  true,
			wantState: true,
		},
		{
			name:      "third check over target 40ms -> continues dropping",
			sojourn:   40 * time.Millisecond,
			sleep:     5 * time.Millisecond,
			wantDrop:  true,
			wantState: true,
		},
		{
			name:      "sojourn recovers under target 10ms -> no drop and clears state",
			sojourn:   10 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "subsequent under target 5ms -> no drop",
			sojourn:   5 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "new burst over target 35ms -> no drop (first observation)",
			sojourn:   35 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "idle gap longer than interval (>100ms) with over target -> no drop (not persistent)",
			sojourn:   35 * time.Millisecond,
			sleep:     120 * time.Millisecond,
			wantDrop:  false,
			wantState: false,
		},
		{
			name:      "immediate subsequent over target 35ms -> drops",
			sojourn:   35 * time.Millisecond,
			sleep:     5 * time.Millisecond,
			wantDrop:  true,
			wantState: true,
		},
	}

	c := NewCodel(target, interval)

	for i, s := range steps {
		if s.sleep > 0 {
			time.Sleep(s.sleep)
		}

		gotDrop := c.ShouldDrop(s.sojourn)
		if gotDrop != s.wantDrop {
			t.Errorf("step %d (%s): ShouldDrop(%v) = %v, want %v", i, s.name, s.sojourn, gotDrop, s.wantDrop)
		}

		if gotState := c.Dropping(); gotState != s.wantState {
			t.Errorf("step %d (%s): Dropping() = %v, want %v", i, s.name, gotState, s.wantState)
		}
	}
}
