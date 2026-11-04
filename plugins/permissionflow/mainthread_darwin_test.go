//go:build darwin

package permissionflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	testwait "github.com/egoist/mygo/internal/wait"
)

// askEnv makes the test binary the helper process of
// TestAGoroutineAsksInOneStep.
const askEnv = "MYGO_PERMISSIONFLOW_TEST_ASK"

func init() {
	if os.Getenv(askEnv) == "1" {
		// The main goroutine stays on the main thread, whose run loop is
		// the main run loop.
		runtime.LockOSThread()
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(askEnv) == "1" {
		os.Exit(askMain())
	}
	os.Exit(m.Run())
}

// askUnits are what a goroutine asks the frameworks in the helper process
// of TestAGoroutineAsksInOneStep.
var askUnits = []struct {
	name string
	ask  func()
}{
	{"Check of Accessibility", func() { check(Accessibility) }},
	{"Check of the microphone", func() { check(Microphone) }},
	{"the usage description", func() { usageDescription("NSMicrophoneUsageDescription") }},
	{"the process of System Settings", func() { settingsProcess() }},
	{"the frame of System Settings", func() { settingsFrame(int32(os.Getpid())) }},
	{"the app", func() { defaultApp() }},
}

// askMain is the helper process of TestAGoroutineAsksInOneStep. On the
// main thread it runs the worker around the main run loop, while a
// goroutine asks each of askUnits and then stops the run loop. It prints
// how many calls each unit handed the main thread.
func askMain() int {
	fail := func(err error) int { fmt.Println(err); return 1 }
	if err := loadCore(); err != nil {
		return fail(err)
	}
	sym := func(name string) uintptr {
		p, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
		if err != nil {
			panic(err)
		}
		return p
	}
	runLoopRun, runLoopStop, getMain := sym("CFRunLoopRun"), sym("CFRunLoopStop"), sym("CFRunLoopGetMain")
	err := mainthread.Run(func() error { return nil }, func() {
		go func() {
			defer mainthread.RunOnMain(func() {
				main, _, _ := purego.SyscallN(getMain)
				mainthread.Forward[struct{}](runLoopStop, main)
			})
			for _, u := range askUnits {
				before := mainthread.Queued()
				u.ask()
				fmt.Printf("%s handed %d calls\n", u.name, mainthread.Queued()-before)
			}
		}()
		purego.SyscallN(runLoopRun)
	}, func() {})
	if err != nil {
		return fail(err)
	}
	return 0
}

// What a goroutine asks the frameworks while the worker runs is one step
// each (mainthread.RunOnMain), the one call that it hands the main thread,
// none of which shows anything: forwarded one by one, the objects of one
// message would be gone for the next.
func TestAGoroutineAsksInOneStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*testwait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), askEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, u := range askUnits {
		if w := u.name + " handed 1 calls\n"; !strings.Contains(string(out), w) {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}

// A struct larger than 16 bytes returns through another entry point of
// the messages on amd64.
func TestStructMessages(t *testing.T) {
	if err := loadCore(); err != nil {
		t.Fatal(err)
	}
	want := nsRect{nsPoint{1, 2}, nsSize{3, 4}}
	withPool(func() {
		if got := sendRect(send(class("NSValue"), "valueWithRect:", want), "rectValue"); got != want {
			t.Errorf("rectValue = %+v, want %+v", got, want)
		}
	})
	if macMajor() < 10 {
		t.Errorf("macOS %d", macMajor())
	}
}
