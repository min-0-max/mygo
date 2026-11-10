//go:build darwin && (arm64 || amd64)

package metal

import (
	"testing"

	"github.com/egoist/mygo/internal/gpu"
)

func expectRootLayerPanic(t *testing.T, reason string, call func()) {
	t.Helper()
	defer func() {
		if got := recover(); got != reason {
			t.Errorf("panic = %v; want %q", got, reason)
		}
	}()
	call()
}

// These faults exercise only the observation ledger. They allocate no native
// layer, acquire no drawable, and cannot turn missing state into a valid event.
func TestRootLayerObservationRejectsInvalidLedger(t *testing.T) {
	for _, tc := range []struct {
		name, reason              string
		sequence, previousAt, now uint64
		wantClocks                int
	}{
		{"sequence overflow", "metal: root-layer observation sequence overflow", ^uint64(0), 7, 8, 0},
		{"zero clock", "metal: root-layer observation clock is zero", 1, 7, 0, 1},
		{"backward clock", "metal: root-layer observation clock moved backward", 1, 7, 6, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clocks, reports := 0, 0
			r := &Renderer{rootObserver: &rootLayerObserver{
				clock:  func() uint64 { clocks++; return tc.now },
				report: func(gpu.RootLayerRecord) { reports++ },
				state:  gpu.RootLayerRecord{Layer: 1, Sequence: tc.sequence, At: tc.previousAt},
			}}
			expectRootLayerPanic(t, tc.reason, func() { r.observeRootLayer(gpu.RootLayerResized) })
			if clocks != tc.wantClocks || reports != 0 {
				t.Fatalf("invalid ledger emitted/read unexpected data: clocks=%d want=%d reports=%d", clocks, tc.wantClocks, reports)
			}
		})
	}
}

func TestRootLayerAttachmentIdentityCannotWrap(t *testing.T) {
	// This owner is deliberately serial: restore the global identity allocator
	// even when the expected failure is missing, preserving other owners.
	previous := rootLayerAttachments.Load()
	t.Cleanup(func() { rootLayerAttachments.Store(previous) })
	rootLayerAttachments.Store(^uint64(0))
	clocks, reports := 0, 0
	r := &Renderer{}
	expectRootLayerPanic(t, "metal: root-layer attachment identity overflow", func() {
		r.ReportRootLayer(func() uint64 { clocks++; return 1 }, func(gpu.RootLayerRecord) { reports++ })
	})
	if clocks != 0 || reports != 0 || r.rootObserver != nil {
		t.Fatalf("overflow attached or emitted an ambiguous stream: clocks=%d reports=%d attached=%v", clocks, reports, r.rootObserver != nil)
	}
}

func TestRootLayerObservationWithoutSubscriptionDoesNothing(t *testing.T) {
	r := &Renderer{}
	r.observeRootLayer(gpu.RootLayerPresentRequested)
	clocks, reports := 0, 0
	r.ReportRootLayer(func() uint64 { clocks++; return 1 }, nil)
	r.observeRootLayer(gpu.RootLayerResized)
	r.ReportRootLayer(nil, func(gpu.RootLayerRecord) { reports++ })
	r.observeRootLayer(gpu.RootLayerTrimmed)
	if clocks != 0 || reports != 0 || r.rootObserver != nil {
		t.Fatalf("incomplete subscription observed state: clocks=%d reports=%d attached=%v", clocks, reports, r.rootObserver != nil)
	}
}

func TestRootLayerAttachmentExhaustionPersistsAfterRecovery(t *testing.T) {
	previous := rootLayerAttachments.Load()
	t.Cleanup(func() { rootLayerAttachments.Store(previous) })
	rootLayerAttachments.Store(^uint64(0))
	clocks, reports := 0, 0
	for attempt := 1; attempt <= 2; attempt++ {
		r := &Renderer{}
		expectRootLayerPanic(t, "metal: root-layer attachment identity overflow", func() {
			r.ReportRootLayer(func() uint64 { clocks++; return 1 }, func(gpu.RootLayerRecord) { reports++ })
		})
		if rootLayerAttachments.Load() != ^uint64(0) || clocks != 0 || reports != 0 || r.rootObserver != nil {
			t.Errorf("exhausted allocator reused identity on attempt %d: allocator=%d clocks=%d reports=%d attached=%v", attempt, rootLayerAttachments.Load(), clocks, reports, r.rootObserver != nil)
		}
	}
}
