//go:build darwin && arm64

package mainstack

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/egoist/mygo/internal/wait"
)

// childEnv names the child that a test runs as a process of its own: the
// main thread of each child runs a mygo app, which ends the process when
// it quits, or ends it as the collector of a JavaScriptCore VM does.
const childEnv = "MYGO_E2E_MAINSTACK_CHILD"

// TestMain runs the tests with MYGO_E2E=1 only, and runs a child on the
// main thread, which mygo locks the main goroutine to, when childEnv names
// one.
func TestMain(m *testing.M) {
	if os.Getenv("MYGO_E2E") == "" {
		fmt.Println("skipping e2e tests; set MYGO_E2E=1 to run them in a desktop session")
		os.Exit(0)
	}
	switch child := os.Getenv(childEnv); child {
	case "":
		os.Exit(m.Run())
	case "collector":
		os.Exit(collectorChild())
	default:
		fmt.Fprintf(os.Stderr, "unknown child %q\n", child)
		os.Exit(2)
	}
}

// runChild runs the test binary as the child named child, and returns
// what it wrote, its output and its errors together, once it ended.
func runChild(t *testing.T, child string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"="+child)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// A child that did not end by the guard ends with the test.
	t.Cleanup(func() { cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	err := wait.For(t, "the "+child+" child to end", done)
	return out.String(), err
}
