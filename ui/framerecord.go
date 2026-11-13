package ui

import "github.com/egoist/mygo/internal/gpu"

// FrameOutcome is how a frame presented.
type FrameOutcome uint8

const (
	// FrameTransaction presented with the Core Animation transaction.
	FrameTransaction = FrameOutcome(gpu.FrameTransaction)
	// FrameImmediate presented at once.
	FrameImmediate = FrameOutcome(gpu.FrameImmediate)
	// FrameStashed waits for a drawable in flight; its command buffer
	// commits later, outside the frame (CommitStashed).
	FrameStashed = FrameOutcome(gpu.FrameStashed)
	// FrameSkipped found no drawable.
	FrameSkipped = FrameOutcome(gpu.FrameSkipped)
	// FrameUndamaged drew nothing, as nothing changed.
	FrameUndamaged = FrameOutcome(gpu.FrameUndamaged)
	// FrameHeld is left to the surface's next frame, as a frame built outside
	// the surface's for a capture is.
	FrameHeld = FrameOutcome(gpu.FrameHeld)
)

// FrameRecord is what a frame reports as it ends. Times are on the clock of
// the FrameRecorder.
type FrameRecord struct {
	// Start and End are when the frame started and ended, and Request when it
	// asked for its presentation, 0 when it did not.
	Start, Request, End uint64
	// DrawableWait is how long the frame waited for a drawable.
	DrawableWait uint64
	// Damage is the pixels the frame changed, and Nodes the elements of the
	// last build pass of the window.
	Damage, Nodes int
	Outcome       FrameOutcome
	// CommandBuffer tells that the frame committed a command buffer, and
	// Committed when it did, 0 when it did not.
	CommandBuffer bool
	Committed     uint64
	// After the frame, the window asks for a frame, animates, paints again,
	// or has a timer armed for a frame.
	AsksFrame, Animates, Repaints, Timer bool
	// Deferred is the inputs that reached the window while the frame built,
	// as a nested step of the main thread delivers them, which the window
	// applied after the frame.
	Deferred int
}

// Settled reports that the window has nothing left that draws a frame.
func (r *FrameRecord) Settled() bool {
	return !r.AsksFrame && !r.Animates && !r.Repaints && !r.Timer
}

// CommitKind is what a command buffer committed outside a frame drew.
type CommitKind uint8

const (
	// CommitStashed is a stashed frame, drawn into its drawable or dropped.
	CommitStashed = CommitKind(gpu.CommitStashed)
	// CommitOffscreen is an offscreen canvas.
	CommitOffscreen = CommitKind(gpu.CommitOffscreen)
	// CommitPixels is a render to pixels.
	CommitPixels = CommitKind(gpu.CommitPixels)
)

// CommitRecord reports a command buffer committed outside a frame.
type CommitRecord struct {
	Kind CommitKind
	At   uint64
}

// CompleteRecord reports a command buffer that completed, a frame's or one
// committed outside a frame: Committed is the Committed of its frame or the
// At of its CommitRecord, and Completed when its completed handler ran.
type CompleteRecord struct {
	Committed, Completed uint64
}

// FrameRecorder hears the frames of a window, each on the thread that drew it.
// Frame must not keep its record, which the next frame writes over. Clock gives
// the time of the records. Complete, when set, hears each command buffer
// complete, on the thread that runs its completed handler, which is none of
// the frame's.
type FrameRecorder struct {
	Clock    func() uint64
	Frame    func(*FrameRecord)
	Commit   func(CommitRecord)
	Complete func(CompleteRecord)
}

// frameReporter is a GPU renderer that tells what came of a frame (Metal's), and
// reports each command buffer it commits outside a frame.
type frameReporter interface {
	ReportFrames(clock func() uint64, commit func(gpu.CommitKind, uint64), complete func(committed, completed uint64))
	FrameFacts() gpu.FrameFacts
}

// RecordFrames has r hear the frames of the windows the content shows, from
// their first frame when called before the window opens. Main thread only.
func (v *Content) RecordFrames(r *FrameRecorder) {
	v.recorder = r
	for _, h := range v.hosts {
		h.record(r)
	}
}

// record has r hear the frames of the window.
func (h *windowHost) record(r *FrameRecorder) {
	h.rt.recorder = r
	h.reportFrames()
}

// reportFrames tells the GPU renderer of the recorder.
func (h *windowHost) reportFrames() {
	f, ok := h.gpu.(frameReporter)
	r := h.rt.recorder
	if !ok || r == nil {
		return
	}
	var complete func(committed, completed uint64)
	if r.Complete != nil {
		complete = func(committed, completed uint64) {
			r.Complete(CompleteRecord{Committed: committed, Completed: completed})
		}
	}
	f.ReportFrames(r.Clock, func(kind gpu.CommitKind, at uint64) {
		r.Commit(CommitRecord{Kind: CommitKind(kind), At: at})
	}, complete)
}

// frameFacts tells what came of the frame presented last.
func (h *windowHost) frameFacts() gpu.FrameFacts { return h.facts }

// frameAsks reports whether the window asked for a frame that has not come.
func (h *windowHost) frameAsks() bool { return h.frameAsked && !h.detached }

// beginRecord starts the record of a frame.
func (rt *engine) beginRecord() {
	r := rt.recorder
	if r == nil {
		return
	}
	rt.record = FrameRecord{Start: r.Clock()}
	rt.recordAsked, rt.recordPresent = 0, false
}

// askPresent notes the frame presenting.
func (rt *engine) askPresent() {
	if r := rt.recorder; r != nil {
		rt.recordAsked, rt.recordPresent = r.Clock(), true
	}
}

// reportFrame ends the record of a frame and reports it. A frame that ended
// before it presented, as its view closed the window, drew nothing.
func (rt *engine) reportFrame() {
	r := rt.recorder
	if r == nil {
		return
	}
	f := &rt.record
	f.Outcome = FrameUndamaged
	if rt.recordPresent {
		facts := rt.host.frameFacts()
		f.Outcome = FrameOutcome(facts.Outcome)
		f.Request, f.DrawableWait = facts.Request, facts.DrawableWait
		f.Damage, f.CommandBuffer, f.Committed = facts.Damage, facts.CommandBuffer, facts.Committed
		if f.Request == 0 && (facts.Outcome == gpu.FrameTransaction || facts.Outcome == gpu.FrameImmediate) {
			f.Request = rt.recordAsked
		}
	}
	f.Nodes = rt.nodes
	f.AsksFrame = rt.host.frameAsks()
	f.Animates = rt.animating
	f.Repaints = rt.repainting
	f.Timer = rt.afterArmed || !rt.repaintDue.IsZero()
	f.End = r.Clock()
	r.Frame(f)
}
