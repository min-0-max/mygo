//go:build darwin

package mygo

import "testing"

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
