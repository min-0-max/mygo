//go:build darwin

package mygo

import "github.com/egoist/mygo/internal/darwin/mainthread"

// closureSender names the function that posts a closure to the main thread
// now, or "" while the steps are not recorded.
func closureSender() string { return mainthread.ClosureSender() }

// runClosure runs fn, which sender posted, and records it as a step of the
// kind StepClosure when sender is not "".
func runClosure(sender string, fn func()) { mainthread.RunClosure(sender, fn) }
