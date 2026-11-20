//go:build darwin

package mainthread

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// frameworkPrefix is the import path of the framework. The sender of a
// closure is the first function of the stack of the post outside it.
var frameworkPrefix = "github.com/egoist/mygo"

// closureRecords holds the closure steps between two drains. It holds at
// most stepSlots of them and counts the closures that it could not hold, as
// the ring of the steps does.
var closureRecords struct {
	sync.Mutex
	steps []Step
	lost  uint64
}

// ClosureSender names the function that posts a closure to a queue of the
// main thread now: the first function of the stack of its caller outside the
// framework, or the outermost function that the stack holds when every
// function is inside it. It returns "" while the steps are not recorded, so
// a post reads one word.
func ClosureSender() string {
	if atomic.LoadUintptr(&link.stepsOn) == 0 {
		return ""
	}
	var pcs [32]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	sender := ""
	for {
		frame, more := frames.Next()
		sender = frame.Function
		if !inFramework(sender) || !more {
			return sender
		}
	}
}

// inFramework reports whether the function name belongs to a package of the
// framework.
func inFramework(name string) bool {
	rest, ok := strings.CutPrefix(name, frameworkPrefix)
	return ok && (rest == "" || rest[0] == '.' || rest[0] == '/')
}

// RunClosure runs fn, a closure that sender posted, on the main thread. With
// a sender it records the run as a step of the kind StepClosure, whose
// Callback is the sender; a drain reports it with the steps
// (DrainSteps).
func RunClosure(sender string, fn func()) {
	if sender == "" {
		fn()
		return
	}
	// The clock is a C function of the worker, which a queue can run before
	// the worker starts; a clock that cannot be resolved counts the closure
	// as lost.
	if loadNative() != nil {
		fn()
		closureRecords.Lock()
		closureRecords.lost++
		closureRecords.Unlock()
		return
	}
	entry := stepNow()
	fn()
	step := Step{Kind: StepClosure, Callback: sender, Entry: entry, Return: stepNow()}
	closureRecords.Lock()
	if len(closureRecords.steps) < stepSlots {
		closureRecords.steps = append(closureRecords.steps, step)
	} else {
		closureRecords.lost++
	}
	closureRecords.Unlock()
}

// stepNow reads the clock of the step records.
func stepNow() uint64 { return direct[uint64](link.clock, uintptr(stepClock)) }

// takeClosures returns the closure steps since the last drain and how many
// were lost, and empties the record.
func takeClosures() ([]Step, uint64) {
	closureRecords.Lock()
	defer closureRecords.Unlock()
	steps, lost := closureRecords.steps, closureRecords.lost
	closureRecords.steps, closureRecords.lost = nil, 0
	return steps, lost
}
