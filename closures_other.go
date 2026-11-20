//go:build !darwin

package mygo

// closureSender records no sender on a platform whose main thread hands no
// steps to a worker.
func closureSender() string { return "" }

// runClosure runs fn.
func runClosure(_ string, fn func()) { fn() }
