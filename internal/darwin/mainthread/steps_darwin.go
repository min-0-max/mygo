//go:build darwin

package mainthread

import (
	"sync"
	"sync/atomic"
)

// StepKind says what a step entered.
type StepKind uint8

const (
	// StepCallback is a C callback.
	StepCallback StepKind = iota
	// StepSelector is the method of a selector.
	StepSelector
	// StepBlock is a block.
	StepBlock
	// StepClosure is a closure that a goroutine posted to a queue of the
	// main thread: its Callback names the function that posted it.
	StepClosure
)

// Step is what the main thread records of a step that entered through a
// trampoline. Its times are of CLOCK_UPTIME_RAW, in nanoseconds.
type Step struct {
	Kind StepKind
	// Callback names what the step entered: the selector of a method whose
	// class RegisterClass registered, otherwise the function that made the
	// trampoline.
	Callback string
	// Entry and Return are when the callback entered and returned on the
	// main thread.
	Entry, Return uint64
	// Forwarded is how many calls the step forwarded to the main thread.
	Forwarded uint64
	// Wait is how long the main thread slept in the step, waiting for the
	// worker. A call that the main thread runs meanwhile is not a wait.
	Wait uint64
}

// stepClock is CLOCK_UPTIME_RAW, the clock of the event log of the host.
const stepClock = 8

// stepSlots is how many steps the ring holds between two drains. It is a
// power of 2.
const stepSlots = 1 << 12

// stepSlot is a step in the ring, which stepDone writes on the main thread.
type stepSlot struct {
	index, entry, ret, forwarded, wait uintptr
}

// stepRingState is a ring with one writer, the main thread, which moves
// head and lost, and one reader, DrainSteps, which moves tail.
type stepRingState struct {
	head, tail, lost uintptr
	slots            [stepSlots]stepSlot
}

var (
	stepRing  stepRingState
	stepDrain struct {
		sync.Mutex
		lost uintptr // the lost steps that a drain reported
	}
)

// stepDone(rec *record) is the C function that records a step that returned.
func stepDone()

// RecordSteps turns the records of the steps on or off. A step reads this
// when it enters. Off, a step reads one word and writes nothing more.
func RecordSteps(on bool) {
	if on {
		atomic.StoreUintptr(&link.stepsOn, 1)
		return
	}
	atomic.StoreUintptr(&link.stepsOn, 0)
}

// DrainSteps reports the steps in the ring in the order of their returns,
// with the closure steps (RunClosure) merged by their returns, and returns
// how many steps it lost since the last drain because the ring or the record
// of the closures was full. A closure that returns while a drain runs is
// reported by the next drain, which can follow a step that contains it.
func DrainSteps(report func(Step)) uint64 {
	stepDrain.Lock()
	defer stepDrain.Unlock()
	head := atomic.LoadUintptr(&stepRing.head)
	closures, closuresLost := takeClosures()
	for tail := stepRing.tail; tail != head; tail++ {
		slot := &stepRing.slots[tail&(stepSlots-1)]
		for len(closures) > 0 && closures[0].Return <= uint64(slot.ret) {
			report(closures[0])
			closures = closures[1:]
		}
		report(Step{
			Kind: StepKind(targetKinds[slot.index]), Callback: targetNames[slot.index],
			Entry: uint64(slot.entry), Return: uint64(slot.ret), Forwarded: uint64(slot.forwarded), Wait: uint64(slot.wait),
		})
		atomic.StoreUintptr(&stepRing.tail, tail+1)
	}
	for _, closure := range closures {
		report(closure)
	}
	lost := atomic.LoadUintptr(&stepRing.lost)
	missed := lost - stepDrain.lost
	stepDrain.lost = lost
	return uint64(missed) + closuresLost
}
