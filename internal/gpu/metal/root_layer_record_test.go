//go:build darwin && (arm64 || amd64)

package metal

import (
	"testing"

	"github.com/egoist/mygo/internal/gpu"
)

// TestRootLayerTransitionsReportActualDrawableSize establishes an owner for
// timestamped property transitions, not a compositor/presentation claim.
func TestRootLayerTransitionsReportActualDrawableSize(t *testing.T) {
	r := layerRenderer(t)
	var records []gpu.RootLayerRecord
	var clock uint64
	r.ReportRootLayer(func() uint64 { clock++; return clock }, func(v gpu.RootLayerRecord) {
		records = append(records, v)
	})
	r.fit(80, 40, 1)
	r.trim()
	r.fit(80, 40, 1)
	if len(records) != 4 {
		t.Fatalf("attach, fit, trim, restore: %+v", records)
	}
	kinds := []gpu.RootLayerKind{gpu.RootLayerAttached, gpu.RootLayerResized, gpu.RootLayerTrimmed, gpu.RootLayerResized}
	for i, v := range records {
		if v.Kind != kinds[i] || v.Sequence != uint64(i+1) || v.At != uint64(i+1) || v.Layer == 0 || v.Layer != records[0].Layer {
			t.Fatalf("transition %d: %+v", i, v)
		}
		if i > 0 {
			w, h := 80, 40
			if v.Kind == gpu.RootLayerTrimmed {
				w, h = 1, 1
			}
			if v.Width != w || v.Height != h {
				t.Fatalf("actual drawable dimensions at %v: %+v", v.Kind, v)
			}
		}
	}
	// No observation means no clock reads, including idle transitions.
	r.ReportRootLayer(nil, nil)
	r.trim()
	r.fit(80, 40, 1)
	if clock != 4 || len(records) != 4 {
		t.Fatalf("detached observer still ran: clock=%d records=%+v", clock, records)
	}
}

func TestRootLayerPresentationRequestHasItsOwnSequence(t *testing.T) {
	r := layerRenderer(t)
	var records []gpu.RootLayerRecord
	var clock uint64
	r.ReportRootLayer(func() uint64 { clock++; return clock }, func(v gpu.RootLayerRecord) { records = append(records, v) })
	if err := r.Render(squareAt(0)); err != nil {
		t.Fatal(err)
	}
	r.trim()
	var request gpu.RootLayerRecord
	for _, v := range records {
		if v.Kind == gpu.RootLayerPresentRequested {
			request = v
		}
	}
	if request.Sequence == 0 || request.LastRequestSequence != request.Sequence || request.LastRequestAt != request.At {
		t.Fatalf("missing presentation REQUEST (not display acknowledgment): %+v", records)
	}
	last := records[len(records)-1]
	if last.Kind != gpu.RootLayerTrimmed || last.LastRequestSequence != request.Sequence || last.LastRequestAt != request.At {
		t.Fatalf("trim lost the prior request identity: %+v", records)
	}
}
