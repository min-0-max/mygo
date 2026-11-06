//go:build darwin

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin"
	"github.com/egoist/mygo/internal/wait"
)

// runActivationApp runs activationApp in mode, a process of its own, and
// returns its output.
func runActivationApp(t *testing.T, mode string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "MYGO_E2E_ACTIVATION_APP="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fronts reads the front process that activationApp printed at each
// point, and its own process.
func fronts(t *testing.T, out string, points ...string) (front map[string]int, self int) {
	t.Helper()
	front = map[string]int{}
	for _, p := range points {
		i := strings.Index(out, p+": front ")
		if i < 0 {
			t.Fatalf("no front process at %s in the output:\n%s", p, out)
		}
		var pid, own int
		if _, err := fmt.Sscanf(out[i+len(p):], ": front %d, this app %d\n", &pid, &own); err != nil {
			t.Fatalf("the front process at %s: %v\n%s", p, err, out)
		}
		front[p], self = pid, own
	}
	return front, self
}

// frontNow returns the front process, as the window server answers it.
func frontNow(t *testing.T) int {
	t.Helper()
	var pid int
	var err error
	mygo.RunOnMain(func() { pid, err = darwin.TestFrontProcess() })
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// With SetSelfActivation(false), neither the launch nor Show activates
// the app: the app that was in front stays there, as the window server
// answers right after each (U5). macOS brings an app that has no window
// to the front only once it shows one, so the activation of the launch
// shows after Show too. The control, the same run by default, shows that
// the window server answers the app's activation at that point: the app
// is in front right after Show. An app other than these two in front
// means that the user switched apps meanwhile: the test says which and
// runs once more.
func TestLaunchWithoutActivation(t *testing.T) {
	for attempt := 1; ; attempt++ {
		before := frontNow(t)
		out, err := runActivationApp(t, "activation-owned")
		if err != nil {
			t.Fatalf("without self-activation: %v\n%s", err, out)
		}
		front, self := fronts(t, out, "launch", "show")
		switched := false
		for _, p := range []string{"launch", "show"} {
			switch front[p] {
			case before:
			case self:
				t.Errorf("without self-activation, the app is in front after the %s (process %d); %d was before", p, self, before)
			default:
				t.Logf("attempt %d: process %d is in front after the %s, neither the app (%d) nor %d, which was before: the user switched apps", attempt, front[p], p, self, before)
				switched = true
			}
		}
		if t.Failed() {
			return
		}
		if !switched {
			break
		}
		if attempt == 2 {
			t.Fatal("the user switched apps during both attempts without self-activation")
		}
	}

	for attempt := 1; ; attempt++ {
		before := frontNow(t)
		out, err := runActivationApp(t, "activation-default")
		if err != nil {
			t.Fatalf("by default: %v\n%s", err, out)
		}
		front, self := fronts(t, out, "show")
		switch front["show"] {
		case self:
			return
		case before:
			t.Fatalf("by default, the app (%d) is not in front right after Show; %d stays there: the window server does not answer the activation at that point\n%s", self, before, out)
		default:
			t.Logf("attempt %d: process %d is in front right after Show by default, neither the app (%d) nor %d, which was before: the user switched apps", attempt, front["show"], self, before)
		}
		if attempt == 2 {
			t.Fatal("the user switched apps during both attempts by default")
		}
	}
}

// SIGTERM quits the app, unless SetSignalQuit(false) leaves it to the
// app: then it ends the process (U6). Neither run activates the app.
func TestSignalsOwnedByTheApplication(t *testing.T) {
	out, err := runActivationApp(t, "signal-default")
	if err != nil {
		t.Fatalf("by default: %v\n%s", err, out)
	}
	if !strings.Contains(out, "quit\n") || !strings.Contains(out, "Run returned\n") {
		t.Errorf("by default, SIGTERM did not quit the app:\n%s", out)
	}
	out, err = runActivationApp(t, "signal-owned")
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

// activationApp is the helper process of the tests above. mode says what
// it does:
//
//   - activation-owned and activation-default: it prints the front process
//     once the launch finished and right after Show showed a window, with
//     SetSelfActivation(false) or by default, and quits.
//   - signal-default and signal-owned: without self-activation, it sends
//     itself SIGTERM once ready, by default or with SetSignalQuit(false).
func activationApp(mode string) {
	if mode != "activation-default" {
		mygo.App.SetSelfActivation(false)
	}
	if mode == "signal-owned" {
		mygo.App.SetSignalQuit(false)
	}
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnQuit(func() { fmt.Println("quit") })
	front := func(at string) {
		pid, err := darwin.TestFrontProcess()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("%s: front %d, this app %d\n", at, pid, os.Getpid())
	}
	mygo.App.WhenReady(func() {
		switch mode {
		case "activation-owned", "activation-default":
			front("launch")
			w := mygo.NewWindow(mygo.WindowOptions{Title: "Activation", Width: 320, Height: 200, Hidden: true})
			w.Show()
			front("show")
			mygo.App.Quit()
		case "signal-default", "signal-owned":
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
		default:
			fmt.Println("unknown MYGO_E2E_ACTIVATION_APP:", mode)
			os.Exit(1)
		}
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Run returned")
	os.Exit(0)
}
