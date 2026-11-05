package mygo

import "github.com/egoist/mygo/internal/callbacks"

// StepKind says what a step of the main thread entered.
type StepKind uint8

const (
	// StepCallback is a C callback.
	StepCallback StepKind = iota
	// StepSelector is the method of a selector.
	StepSelector
	// StepBlock is a block.
	StepBlock
)

// StepRecord is what the main thread records of a step that it handed to
// the worker. Its times are nanoseconds of CLOCK_UPTIME_RAW.
type StepRecord struct {
	Kind StepKind
	// Callback names what the step entered: a selector, or the function
	// that registered the callback or the block.
	Callback string
	// Entry and Return are when the callback entered and returned on the
	// main thread.
	Entry, Return uint64
	// Forwarded is how many calls the step forwarded to the main thread.
	Forwarded uint64
	// Wait is how long the main thread slept in the step, waiting for the
	// worker.
	Wait uint64
}

// RegisteredCallbacks returns how many callbacks the framework registered
// through purego: each purego.NewCallback that it calls, each method of
// each class that it registers with objc.RegisterClass and each block
// function type that it passes to objc.NewBlock for the first time.
func RegisteredCallbacks() int64 { return callbacks.Count() }
