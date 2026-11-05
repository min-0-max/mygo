// Package callbacks counts the callbacks that the framework registers
// through purego, of which purego makes at most 2000 in a process and
// never frees them.
package callbacks

import "sync/atomic"

var count atomic.Int64

// Add counts n callbacks more.
func Add(n int64) { count.Add(n) }

// Count returns how many callbacks the framework registered: each
// purego.NewCallback that it calls, each method of each class that it
// registers with objc.RegisterClass and each block function type that it
// passes to objc.NewBlock for the first time.
func Count() int64 { return count.Load() }
