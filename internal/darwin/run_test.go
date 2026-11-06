//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/wait"
)

// runEnv makes the test binary the helper process of
// TestRunRunsTheEventLoopOfTheApplication.
const runEnv = "MYGO_DARWIN_TEST_RUN"

// runMain is the helper process of TestRunRunsTheEventLoopOfTheApplication.
// On the main thread it runs Backend.Run with a start that makes the
// shared application, as Init does, without a window and with activation
// prohibited, and starts a goroutine whose RunOnMain runs only once the
// main thread runs its run loop, and quits. It prints whether that
// function ran.
func runMain() int {
	const activationProhibited = 2 // NSApplicationActivationPolicyProhibited
	load()
	if !mainthread.IsMainThread() {
		fmt.Println("the helper process does not run on the main thread")
		return 1
	}
	b := &Backend{}
	ran := false
	err := b.Run(func() error {
		b.app = send(class("NSApplication"), "sharedApplication")
		send(b.app, "setActivationPolicy:", activationProhibited)
		go mainthread.RunOnMain(func() {
			ran = true
			b.Quit()
		})
		return nil
	}, func() {})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("the event loop ran the function:", ran)
	return 0
}

// Backend.Run runs the event loop of the application that start makes:
// the function that a goroutine runs through RunOnMain while it runs, and
// that quits, runs before Run returns.
func TestRunRunsTheEventLoopOfTheApplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), runEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if w := "the event loop ran the function: true\n"; !strings.Contains(string(out), w) {
		t.Errorf("no %q in the output:\n%s", w, out)
	}
}
