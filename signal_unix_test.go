//go:build unix

package mygo

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func init() { signalTestMain = signalApp }

// SIGTERM quits the application as Quit does, unless SetSignalQuit(false)
// leaves the signals to the application: then Run handles none, and the
// signal ends the process with its default action (U6).
func TestSignalQuit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode string) (string, error) {
		cmd := exec.Command(exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), "MYGO_TEST_SIGNAL="+mode)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("default")
	if err != nil {
		t.Fatalf("by default: %v\n%s", err, out)
	}
	if !strings.Contains(out, "quit\n") || !strings.Contains(out, "Run returned\n") {
		t.Errorf("by default, SIGTERM did not quit the app:\n%s", out)
	}

	out, err = run("owned")
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("with SetSignalQuit(false): %v, want the process ended by SIGTERM\n%s", err, out)
	}
	if ws := exit.Sys().(syscall.WaitStatus); !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("with SetSignalQuit(false): %v, want the process ended by SIGTERM\n%s", err, out)
	}
	if strings.Contains(out, "quit\n") {
		t.Errorf("with SetSignalQuit(false), SIGTERM quit the app:\n%s", out)
	}
}

// signalApp is a helper process for TestSignalQuit: it runs the app and,
// once it is ready, sends itself SIGTERM. MYGO_TEST_SIGNAL=owned calls
// SetSignalQuit(false) first.
func signalApp() {
	if os.Getenv("MYGO_TEST_SIGNAL") == "owned" {
		App.SetSignalQuit(false)
	}
	App.OnQuit(func() { fmt.Println("quit") })
	App.WhenReady(func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	})
	if err := App.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Run returned")
	os.Exit(0)
}
