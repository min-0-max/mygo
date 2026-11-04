// Package offworker holds functions that other goroutines call while the
// worker runs, which the test of the reader names: each makes its native
// work one main call, or makes no forwarded call, or is a finding.
package offworker

import (
	"callsites"

	"github.com/ebitengine/purego/objc"
)

func sel(name string) objc.SEL { return objc.RegisterName(name) }

// oneMainCall makes its native work one main call, inside which a result
// passes from one call to the next.
func oneMainCall(obj uintptr) uintptr {
	var frame uintptr
	callsites.RunOnMain(func() {
		window := callsites.Send(obj, sel("window"))
		frame = callsites.Send(window, sel("frame"))
	})
	return frame
}

// noForward makes no forwarded call.
func noForward(objs []uintptr) int {
	n := 0
	for range objs {
		n++
	}
	return n
}

// twoForwards makes two forwarded calls.
func twoForwards(obj uintptr) {
	callsites.Send(obj, sel("frame"))  // want: forwards a call outside RunOnMain, off the worker in twoForwards
	callsites.Send(obj, sel("bounds")) // want: forwards a call outside RunOnMain, off the worker in twoForwards
}

// reused passes the result of a forwarded call to another.
func reused(obj uintptr) {
	window := callsites.Send(obj, sel("window")) // want: forwards a call outside RunOnMain, off the worker in reused
	callsites.Send(window, sel("close"))         // want: forwards the result of the forwarded call at offworker.go:42, off the worker in reused
}

// twoMainCalls makes two main calls.
func twoMainCalls(obj uintptr) {
	callsites.RunOnMain(func() { callsites.Send(obj, sel("frame")) })
	callsites.RunOnMain(func() { callsites.Send(obj, sel("bounds")) }) // want: makes a second RunOnMain, off the worker in twoMainCalls
}

// mainInLoop makes a main call for each object.
func mainInLoop(objs []uintptr) {
	for _, o := range objs {
		callsites.RunOnMain(func() { callsites.Send(o, sel("close")) }) // want: makes RunOnMain in a loop, off the worker in mainInLoop
	}
}

// throughHelpers reaches forwarded calls through a function that only it
// calls, and through one that the worker calls too.
func throughHelpers(obj uintptr) {
	frameOf(obj)
	boundsOf(obj) // want: forwards a call outside RunOnMain, off the worker in throughHelpers
}

func frameOf(obj uintptr) uintptr {
	return callsites.Send(obj, sel("frame")) // want: forwards a call outside RunOnMain, off the worker in throughHelpers
}

func boundsOf(obj uintptr) uintptr { return callsites.Send(obj, sel("bounds")) }

// onTheWorker runs on the worker.
func onTheWorker(obj uintptr) uintptr { return boundsOf(obj) }
