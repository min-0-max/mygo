// Package mainstack tests that the main thread of a mygo app runs no code
// on a stack other than its own. The collector of a JavaScriptCore VM
// scans each thread that it registered from the thread's stack pointer to
// the origin of its stack; a main thread that runs Go code runs it on a
// goroutine stack, which the Go heap holds, and a collection that suspends
// it there ends the process.
//
// The tests need a desktop session and run with MYGO_E2E=1, on macOS on
// arm64:
//
//	MYGO_E2E=1 go test ./internal/e2e/mainstack
package mainstack
