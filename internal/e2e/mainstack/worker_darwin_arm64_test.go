//go:build darwin && arm64

package mainstack

import (
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/gpu/metal"
)

// mainSend sends a message to recv on the main thread and returns its
// result: in a step of the worker, in a function that mygo.RunOnMain runs
// or a callback of mygo, the main thread runs it as one forwarded call,
// and a callback of mygo that it reaches runs on the worker as a step
// nested in that one.
func mainSend[R any](recv objc.ID, s objc.SEL, args ...any) R {
	return mainthread.Send[R](recv, s, args...)
}

// forwarded returns how many calls the worker and other threads forwarded
// to the main thread so far.
func forwarded() uint64 { return mainthread.Forwarded() }

// onPresent has the renderers call fn with their layer and the drawable
// of each frame before it requests its presentation, until it is called
// with nil. In a step.
func onPresent(fn func(layer, drawable uintptr)) { metal.TestPresenting = fn }
