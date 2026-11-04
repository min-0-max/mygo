//go:build darwin && !mainthreadcheck

package mainthread

import "github.com/ebitengine/purego"

// Bind binds the function variable that fn points to to the native
// function name of lib, which runs on the calling thread. Built with the
// tag mainthreadcheck, it binds a function that checks each call against
// the lists of this package first; here it binds the same function as
// purego.RegisterLibFunc.
func Bind(fn any, lib uintptr, name string) { purego.RegisterLibFunc(fn, lib, name) }

// BindAddr is Bind for the native function name at addr.
func BindAddr(fn any, addr uintptr, name string) { purego.RegisterFunc(fn, addr) }

// Check replaces the function that fn points to, which calls the native
// function name on the calling thread, with one that checks each call.
// Here it does nothing.
func Check(fn any, name string) {}

// CheckSend checks a message that runs on the calling thread, before a
// call site that sends it without a bound function. Here it does nothing.
func CheckSend(recv, sel uintptr) {}

// checkCalling checks a call that ByReceiver runs on the calling thread.
func checkCalling(fn uintptr, args []uintptr) {}

// checkedOnMain returns the record to forward for rec.
func checkedOnMain(rec *record) *record { return rec }

// checkedRunOnMain returns the function that RunOnMain runs for fn.
func checkedRunOnMain(fn func()) func() { return fn }

// checkedSignal returns the function that wakes the other side for the
// address of semaphore_signal.
func checkedSignal(signal uintptr) uintptr { return signal }

// noteEntry takes the target of a new trampoline, and whether it is the
// implementation of a method.
func noteEntry(target uintptr, method bool) {}
