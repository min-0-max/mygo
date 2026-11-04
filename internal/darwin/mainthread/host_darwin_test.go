//go:build darwin

package mainthread

import (
	"runtime"
	"testing"

	"github.com/ebitengine/purego/objc"
)

// The C functions of RunOnWorkerAddress and RunOnMainAddress run a block
// on the worker from the main thread and on the main thread from the
// worker during a step, and at once on any other thread.
func TestTheHostRunsBlocksOnTheWorkerAndOnTheMainThread(t *testing.T) {
	self := pthreadSelf(t)
	var ranOn uintptr
	var counted bool
	block := objc.NewBlock(func(objc.Block) {
		ranOn = direct[uintptr](self)
		counted = IsMainThread()
	})
	defer block.Release()
	onWorker, onMain := RunOnWorkerAddress(), RunOnMainAddress()
	run := func(fn uintptr) (uintptr, bool) {
		ranOn, counted = 0, false
		direct[struct{}](fn, block)
		return ranOn, counted
	}

	here := make(chan [2]uintptr)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		a, _ := run(onWorker)
		b, _ := run(onMain)
		here <- [2]uintptr{a, b}
	}()
	if got := <-here; got[0] == 0 || got[0] != got[1] {
		t.Errorf("while the worker does not run, the blocks ran on %#x and %#x, not at once on the calling thread", got[0], got[1])
	}

	onFakeMain(t, func() {
		main := direct[uintptr](self)
		var worker uintptr
		RunOnWorker(func() { worker = direct[uintptr](self) })

		if on, step := run(onWorker); on != worker || !step {
			t.Errorf("from the main thread the block ran on %#x (IsMainThread %v), not on the worker %#x as a step", on, step, worker)
		}
		var toMain, onItself, other, otherRan uintptr
		RunOnWorker(func() {
			toMain, _ = run(onMain)
			onItself, _ = run(onWorker)
			done := make(chan struct{})
			go func() {
				defer close(done)
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				otherRan, _ = run(onMain)
				other = direct[uintptr](self)
			}()
			<-done
		})
		if toMain != main {
			t.Errorf("from the worker the block ran on %#x, not on the main thread %#x", toMain, main)
		}
		if onItself != worker {
			t.Errorf("on the worker the block of the worker ran on %#x, not at once on the worker %#x", onItself, worker)
		}
		if otherRan != other || otherRan == main {
			t.Errorf("on another thread %#x the block of the main thread ran on %#x, not at once", other, otherRan)
		}
	})
}
