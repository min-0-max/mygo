//go:build darwin

package metal

import (
	"github.com/egoist/mygo/internal/gpu"
	"sync/atomic"
)

var rootLayerAttachments atomic.Uint64

type rootLayerObserver struct {
	clock  func() uint64
	report func(gpu.RootLayerRecord)
	state  gpu.RootLayerRecord
}

// ReportRootLayer explicitly attaches root-layer transition observation.
// Both callbacks are required; nil detaches. It never acquires a drawable,
// redraws, or interprets a presentation request as a displayed frame.
func (r *Renderer) ReportRootLayer(clock func() uint64, report func(gpu.RootLayerRecord)) {
	r.rootObserver = nil
	if clock == nil || report == nil {
		return
	}
	// Saturate at exhaustion: recovering an observer panic must never make
	// a later attachment reuse an earlier stream identity.
	var layer uint64
	for {
		previous := rootLayerAttachments.Load()
		if previous == ^uint64(0) {
			panic("metal: root-layer attachment identity overflow")
		}
		if rootLayerAttachments.CompareAndSwap(previous, previous+1) {
			layer = previous + 1
			break
		}
	}
	w, h := r.w, r.h
	if r.trimmed {
		w, h = 1, 1
	}
	r.rootObserver = &rootLayerObserver{clock: clock, report: report,
		state: gpu.RootLayerRecord{Layer: layer, Width: w, Height: h, SizeKnown: r.trimmed || (w > 0 && h > 0)}}
	r.observeRootLayer(gpu.RootLayerAttached)
}

func (r *Renderer) observeRootLayer(kind gpu.RootLayerKind) {
	o := r.rootObserver
	if o == nil {
		return
	}
	if o.state.Sequence == ^uint64(0) {
		panic("metal: root-layer observation sequence overflow")
	}
	previousAt := o.state.At
	o.state.Sequence++
	o.state.Kind, o.state.At = kind, o.clock()
	if o.state.At == 0 {
		panic("metal: root-layer observation clock is zero")
	}
	if o.state.At < previousAt {
		panic("metal: root-layer observation clock moved backward")
	}
	if kind != gpu.RootLayerAttached {
		o.state.Width, o.state.Height, o.state.SizeKnown = r.w, r.h, true
		if r.trimmed {
			o.state.Width, o.state.Height = 1, 1
		}
	}
	if kind == gpu.RootLayerPresentRequested {
		o.state.LastRequestSequence, o.state.LastRequestAt = o.state.Sequence, o.state.At
	}
	o.report(o.state)
}
