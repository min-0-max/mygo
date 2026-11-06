//go:build darwin

package mygo

import "github.com/egoist/mygo/internal/darwin/mainthread"

// RunOnWorkerAddress returns the address of one of the two functions that
// give C code the worker of the main thread: a C function that takes a
// block that takes no arguments. Called on the main thread while the
// worker runs, it runs the block on the worker as a step, waits for it
// and meanwhile runs the calls that the worker forwards. Called on any
// other thread, it runs the block at once.
func RunOnWorkerAddress() uintptr { return mainthread.RunOnWorkerAddress() }

// RunOnMainAddress returns the address of the other function that gives C
// code the worker of the main thread: a C function that takes a block that
// takes no arguments. Called on the worker during a step, it runs the
// block on the main thread. Called on any other thread, it runs the block
// at once.
func RunOnMainAddress() uintptr { return mainthread.RunOnMainAddress() }
