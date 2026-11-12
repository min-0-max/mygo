package ui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/internal/gpu"
)

// recorded keeps the records of a window.
type recorded struct {
	frames    []FrameRecord
	commits   []CommitRecord
	completes []CompleteRecord
	clock     uint64
}

func (r *recorded) recorder() *FrameRecorder {
	return &FrameRecorder{
		Clock:    func() uint64 { r.clock++; return r.clock },
		Frame:    func(f *FrameRecord) { r.frames = append(r.frames, *f) },
		Commit:   func(c CommitRecord) { r.commits = append(r.commits, c) },
		Complete: func(c CompleteRecord) { r.completes = append(r.completes, c) },
	}
}

// TestAFrameReportsOneRecordAndASettledWindowNone checks that each frame that
// starts reports once, and that a window with nothing to do reports nothing.
func TestAFrameReportsOneRecordAndASettledWindowNone(t *testing.T) {
	back := RGB(255, 255, 255)
	tt := NewTester(func(c *Context) {
		Column(c).Size(200, 100).Background(back).Children(func() {
			Text(c, "one")
			Text(c, "two")
		})
	}, 200, 100)
	var r recorded
	tt.rt.recorder = r.recorder()
	// The Tester drew the window as it started: a frame drawing it all
	// changes all of it.
	back = RGB(240, 240, 240)
	tt.Frame()
	if len(r.frames) != 1 {
		t.Fatalf("one frame reported %d records", len(r.frames))
	}
	f := r.frames[0]
	if f.Start == 0 || f.Request < f.Start || f.End < f.Request {
		t.Errorf("times start %d, request %d, end %d", f.Start, f.Request, f.End)
	}
	if f.Nodes < 3 {
		t.Errorf("%d nodes in a build of a column and two texts", f.Nodes)
	}
	if f.Outcome != FrameImmediate || f.CommandBuffer {
		t.Errorf("a frame drawn in memory has the outcome %v and a command buffer %v", f.Outcome, f.CommandBuffer)
	}
	if f.Damage != 200*100 {
		t.Errorf("damage %d of a frame of 200×100", f.Damage)
	}
	if !f.Settled() {
		t.Errorf("a window with nothing to do is not settled: %+v", f)
	}
	tt.Advance(time.Hour)
	if len(r.frames) != 1 {
		t.Fatalf("a settled window reported %d records in an hour", len(r.frames)-1)
	}
}

// TestATesterRecordsTheDamageOfAFrame checks that a frame in memory tells the
// pixels it changed, as a window's do: those of a box that changes, none when
// nothing did, all of them when the window's background changes.
func TestATesterRecordsTheDamageOfAFrame(t *testing.T) {
	box, back := RGB(200, 0, 0), RGB(255, 255, 255)
	tt := NewTester(func(c *Context) {
		Column(c).Size(200, 100).Background(back).Children(func() {
			Box(c).Absolute().Left(10).Top(10).Size(20, 20).Background(box)
		})
	}, 200, 100)
	var r recorded
	tt.RecordFrames(r.recorder())
	box = RGB(0, 200, 0)
	tt.Frame()
	tt.Frame()
	back = RGB(240, 240, 240)
	tt.Frame()
	if len(r.frames) != 3 {
		t.Fatalf("three frames reported %d records", len(r.frames))
	}
	if f := r.frames[0]; f.Damage < 20*20 || f.Damage > 24*24 || f.Outcome != FrameImmediate {
		t.Errorf("a box of 20×20 changing: damage %d, outcome %v", f.Damage, f.Outcome)
	}
	if f := r.frames[1]; f.Damage != 0 || f.Outcome != FrameUndamaged {
		t.Errorf("nothing changing: damage %d, outcome %v", f.Damage, f.Outcome)
	}
	if f := r.frames[2]; f.Damage != 200*100 || f.Outcome != FrameImmediate {
		t.Errorf("the background changing: damage %d of 200×100, outcome %v", f.Damage, f.Outcome)
	}
}

// TestARecordSettlesAfterTheLastFrameOfATransition checks that what still moves
// keeps a window from settling, up to the record of its last frame.
func TestARecordSettlesAfterTheLastFrameOfATransition(t *testing.T) {
	on := false
	tt := NewTester(func(c *Context) { Switch(c, &on) }, 200, 100)
	var r recorded
	tt.rt.recorder = r.recorder()
	tt.Key(0, KeyTab)
	tt.Key(0, KeySpace)
	tt.Advance(time.Minute)
	last := len(r.frames) - 1
	if last < 2 {
		t.Fatalf("a transition drew %d frames", last+1)
	}
	if !r.frames[last].Settled() {
		t.Errorf("the last frame of the transition is not settled: %+v", r.frames[last])
	}
	moved := false
	for _, f := range r.frames[:last] {
		if f.Settled() {
			continue
		}
		moved = moved || f.Animates && f.AsksFrame
	}
	if !moved {
		t.Error("no frame of the transition reported that it animates and asks for a frame")
	}
}

// TestARecordSettlesAfterTheLastBlinkOfACaret checks that a timer armed for a
// frame keeps a window from settling, and that its record tells so.
func TestARecordSettlesAfterTheLastBlinkOfACaret(t *testing.T) {
	name := ""
	var input Rect
	tt := NewTester(func(c *Context) { input = TextInput(c, &name).Width(160).Bounds() }, 200, 100)
	var r recorded
	tt.rt.recorder = r.recorder()
	tt.ClickAt(input.X+input.W/2, input.Y+input.H/2)
	tt.Advance(time.Minute)
	last := len(r.frames) - 1
	if last < 2 {
		t.Fatalf("a caret drew %d frames", last+1)
	}
	if !r.frames[last].Settled() {
		t.Errorf("the last frame of the caret is not settled: %+v", r.frames[last])
	}
	armed := false
	for _, f := range r.frames[:last] {
		armed = armed || f.Timer
	}
	if !armed {
		t.Error("no frame of the caret reported a timer armed")
	}
}

// TestRepaintFramesReportRecords checks that a frame that only paints again
// counts as a frame, with the nodes of the last build pass.
func TestRepaintFramesReportRecords(t *testing.T) {
	tt := NewTester(func(c *Context) { Text(c, "one") }, 200, 100)
	var r recorded
	tt.rt.recorder = r.recorder()
	tt.Frame()
	nodes := r.frames[0].Nodes
	tt.rt.repaintFrame(200, 100, 1)
	if len(r.frames) != 2 {
		t.Fatalf("a frame that paints again reported %d records", len(r.frames)-1)
	}
	if r.frames[1].Nodes != nodes {
		t.Errorf("a frame that paints again reported %d nodes, the build %d", r.frames[1].Nodes, nodes)
	}
}

// TestFrameRecordsAllocateNothing checks what a frame pays for the records: no
// allocation without a recorder, and none for the record with one.
func TestFrameRecordsAllocateNothing(t *testing.T) {
	tt := NewTester(func(c *Context) { Text(c, "one") }, 200, 100)
	if n := testing.AllocsPerRun(100, func() { tt.rt.beginRecord(); tt.rt.reportFrame() }); n != 0 {
		t.Errorf("without a recorder: %v allocations a frame", n)
	}
	reports := 0
	tt.rt.recorder = &FrameRecorder{Clock: func() uint64 { return 1 }, Frame: func(*FrameRecord) { reports++ }}
	if n := testing.AllocsPerRun(100, func() { tt.rt.beginRecord(); tt.rt.reportFrame() }); n != 0 {
		t.Errorf("with a recorder: %v allocations a frame", n)
	}
	if reports == 0 {
		t.Error("the recorder heard of no frame")
	}
}

// factsGPU is a GPU renderer that tells what came of its frames.
type factsGPU struct {
	testGPU
	facts    gpu.FrameFacts
	clock    func() uint64
	commit   func(gpu.CommitKind, uint64)
	complete func(committed, completed uint64)
}

func (g *factsGPU) ReportFrames(clock func() uint64, commit func(gpu.CommitKind, uint64), complete func(committed, completed uint64)) {
	g.clock, g.commit, g.complete = clock, commit, complete
}
func (g *factsGPU) FrameFacts() gpu.FrameFacts { return g.facts }

// TestAWindowTellsTheFactsOfItsRenderer checks the record of a frame that a GPU
// renderer drew, and that each commit outside a frame reaches the recorder.
func TestAWindowTellsTheFactsOfItsRenderer(t *testing.T) {
	g := &factsGPU{facts: gpu.FrameFacts{Outcome: gpu.FrameTransaction, Request: 7, Damage: 30, DrawableWait: 9, CommandBuffer: true, Committed: 6}}
	h, _, frame := gpuHost(t, func() (gpuRenderer, error) { return g, nil })
	var r recorded
	h.record(r.recorder())
	frame()
	if len(r.frames) != 1 {
		t.Fatalf("one frame reported %d records", len(r.frames))
	}
	f := r.frames[0]
	if f.Outcome != FrameTransaction || f.Request != 7 || f.Damage != 30 || f.DrawableWait != 9 || !f.CommandBuffer || f.Committed != 6 {
		t.Errorf("the record %+v, the renderer told %+v", f, g.facts)
	}
	if g.clock == nil || g.commit == nil || g.complete == nil {
		t.Fatal("the renderer was not told of the recorder")
	}
	g.commit(gpu.CommitStashed, 11)
	if len(r.commits) != 1 || r.commits[0].Kind != CommitStashed || r.commits[0].At != 11 {
		t.Errorf("the commits %+v", r.commits)
	}
	g.complete(6, 13)
	if len(r.completes) != 1 || r.completes[0] != (CompleteRecord{Committed: 6, Completed: 13}) {
		t.Errorf("the completions %+v", r.completes)
	}
	// A recorder that hears no completions has the renderer report none.
	h.record(&FrameRecorder{Clock: func() uint64 { return 1 }, Frame: func(*FrameRecord) {}, Commit: func(CommitRecord) {}})
	if g.complete != nil {
		t.Error("a recorder without Complete has the renderer report completions")
	}
}

// TestAFrameOutsideTheSurfaceIsHeld checks the record of a frame that a capture
// built, which presents nothing and leaves the showing to the surface's next.
func TestAFrameOutsideTheSurfaceIsHeld(t *testing.T) {
	g := &factsGPU{facts: gpu.FrameFacts{Outcome: gpu.FrameImmediate, Request: 7, CommandBuffer: true}}
	h, _, frame := gpuHost(t, func() (gpuRenderer, error) { return g, nil })
	frame()
	var r recorded
	h.rt.recorder = r.recorder()
	h.rt.runFrame()
	if len(r.frames) != 1 {
		t.Fatalf("one frame reported %d records", len(r.frames))
	}
	if f := r.frames[0]; f.Outcome != FrameHeld || f.Request != 0 || f.CommandBuffer || !f.AsksFrame {
		t.Errorf("the record %+v of a frame outside the surface's", f)
	}
}
