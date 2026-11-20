//go:build !js

package mygo

import (
	"testing"
	"time"

	"github.com/egoist/mygo/internal/wait"
)

// TestLoopInsideQueuedFunction checks that a function on the main thread
// running a loop of its own, as a context menu does, runs the functions
// queued behind it, which it may be waiting for.
func TestLoopInsideQueuedFunction(t *testing.T) {
	done := make(chan struct{})
	waited := make(chan bool, 1)
	// Both at once, as when a test asks for the menu just as it opens.
	loop.mu.Lock()
	first := len(loop.queue) == 0
	loop.queue = append(loop.queue, queuedClosure{fn: func() {
		for deadline := time.Now().Add(wait.Guard); time.Now().Before(deadline); {
			select {
			case <-done:
				waited <- true
				return
			default:
			}
			backend().Step()
		}
		waited <- false
	}}, queuedClosure{fn: func() { close(done) }})
	loop.mu.Unlock()
	if first {
		backend().Signal()
	}
	if !<-waited {
		t.Fatal("the function queued behind one running a loop did not run")
	}
}

// TestAPostedFunctionIsRecordedWithItsSender checks that a function that
// a goroutine runs on the main thread is a step of the kind StepClosure
// while the steps are recorded, named after the first function outside the
// framework that posted it: the functions of the framework on the way, as
// RunOnMain and this test are, are passed over.
func TestAPostedFunctionIsRecordedWithItsSender(t *testing.T) {
	var closures []StepRecord
	stop := RecordSteps(func(step StepRecord) {
		if step.Kind == StepClosure {
			closures = append(closures, step)
		}
	}, func(uint64) {})
	ran := false
	RunOnMain(func() { ran = true })
	stop()
	if !ran {
		t.Fatal("the posted function did not run")
	}
	if len(closures) == 0 {
		t.Fatal("the posted function was not recorded")
	}
	if got := closures[len(closures)-1]; got.Callback != "testing.tRunner" || got.Return < got.Entry {
		t.Fatalf("the closure step is %+v, want the sender testing.tRunner", got)
	}
}
