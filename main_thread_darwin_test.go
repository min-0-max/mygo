//go:build darwin

package mygo

import (
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// The two C functions of the worker of the main thread are functions of
// their own: code that calls one for the other runs its block on the wrong
// thread.
func TestTheMainThreadFunctionsHaveAddresses(t *testing.T) {
	worker, main := RunOnWorkerAddress(), RunOnMainAddress()
	if worker == 0 || main == 0 {
		t.Fatalf("RunOnWorkerAddress = %#x, RunOnMainAddress = %#x", worker, main)
	}
	if worker == main {
		t.Fatalf("both are %#x", worker)
	}
}

func TestRegisteredCallbacksCountsTheCallbacksOfTheFramework(t *testing.T) {
	before := RegisteredCallbacks()
	mainthread.Callback(purego.NewCallback(func() uintptr { return 0 }))
	if got := RegisteredCallbacks(); got != before+1 {
		t.Fatalf("a callback took the count from %d to %d", before, got)
	}
}
