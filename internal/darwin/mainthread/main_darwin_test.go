//go:build darwin

package mainthread

import (
	"os"
	"runtime"
	"testing"
)

// helpers are the helper processes of the package's tests, which run on
// the main thread, by the environment variable that makes the test
// binary one.
var helpers = map[string]func() int{
	loopEnv:   loopMain,
	appKitEnv: appKitMain,
}

func init() {
	for env := range helpers {
		if os.Getenv(env) == "1" {
			// The main goroutine stays on the main thread, whose run loop
			// is the main run loop.
			runtime.LockOSThread()
		}
	}
}

func TestMain(m *testing.M) {
	for env, main := range helpers {
		if os.Getenv(env) == "1" {
			os.Exit(main())
		}
	}
	os.Exit(m.Run())
}
