package ui

import (
	"github.com/egoist/mygo/internal/gpu"
	"testing"
)

type rootLayerGPU struct {
	factsGPU
	rootClock func() uint64
	root      func(gpu.RootLayerRecord)
}

func (g *rootLayerGPU) ReportRootLayer(clock func() uint64, report func(gpu.RootLayerRecord)) {
	g.rootClock, g.root = clock, report
}

func TestRootLayerRecorderIsExplicitlyOwnedAndDetachable(t *testing.T) {
	g := &rootLayerGPU{}
	h, _, frame := gpuHost(t, func() (gpuRenderer, error) { return g, nil })
	h.record(&FrameRecorder{Clock: func() uint64 { return 7 }, Frame: func(*FrameRecord) {}})
	frame() // The renderer is created by the first frame, after deferred recorder configuration.
	if g.root != nil || g.rootClock != nil {
		t.Fatal("ordinary frame recording enabled root-layer observations")
	}
	var got []RootLayerRecord
	h.record(&FrameRecorder{Clock: func() uint64 { return 9 }, RootLayer: func(v RootLayerRecord) { got = append(got, v) }})
	if g.root == nil || g.rootClock == nil || g.rootClock() != 9 {
		t.Fatal("explicit root-layer recorder was not connected")
	}
	v := gpu.RootLayerRecord{Kind: gpu.RootLayerTrimmed, Layer: 4, Sequence: 8, At: 9, Width: 1, Height: 1, LastRequestSequence: 6, LastRequestAt: 7}
	g.root(v)
	if len(got) != 1 || got[0] != RootLayerRecord(v) {
		t.Fatalf("root transition changed across public recorder: %+v", got)
	}
	h.record(nil)
	if g.root != nil || g.rootClock != nil {
		t.Fatal("removing recording retained a root-layer observer")
	}
}
