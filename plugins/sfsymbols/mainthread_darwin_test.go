//go:build darwin

package sfsymbols

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	testwait "github.com/egoist/mygo/internal/wait"
)

// drawEnv makes the test binary the helper process of
// TestAGoroutineDrawsInOneStep.
const drawEnv = "MYGO_SFSYMBOLS_TEST_DRAW"

func init() {
	if os.Getenv(drawEnv) == "1" {
		// The main goroutine stays on the main thread, whose run loop is
		// the main run loop.
		runtime.LockOSThread()
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(drawEnv) == "1" {
		os.Exit(drawMain())
	}
	os.Exit(m.Run())
}

// drawUnits are what a goroutine asks of AppKit in the helper process of
// TestAGoroutineDrawsInOneStep, each with whether it got what it asked.
var drawUnits = []struct {
	name string
	draw func() bool
}{
	{"a symbol", func() bool {
		coverage, ok, _ := rasterize("gearshape", 48, false, Regular)
		return ok && inkOf(coverage).Dx() >= 40
	}},
	{"a name the system has not", func() bool {
		_, ok, absent := rasterize("no.symbol.of.that.name", 48, false, Regular)
		return !ok && absent
	}},
	{"the names", func() bool { return len(Names()) >= 1000 }},
	{"the names by release", func() bool {
		var list []string
		mainthread.RunOnMain(func() {
			withPool(func() { list = readAvailable() })
		})
		return len(list) >= 1000
	}},
	{"a symbol inside a step", func() bool {
		var drew bool
		mainthread.RunOnMain(func() {
			coverage, ok, _ := rasterize("xmark", 48, false, Regular)
			drew = ok && inkOf(coverage).Dx() >= 40
		})
		return drew
	}},
}

// drawMain is the helper process of TestAGoroutineDrawsInOneStep. On the
// main thread it runs the worker around the main run loop, while a
// goroutine asks each of drawUnits and then stops the run loop. It prints
// how many calls each unit handed the main thread, and whether it drew.
func drawMain() int {
	fail := func(err error) int { fmt.Println(err); return 1 }
	if !loadCG() {
		return fail(fmt.Errorf("no AppKit here"))
	}
	sym := func(name string) uintptr {
		p, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
		if err != nil {
			panic(err)
		}
		return p
	}
	runLoopRun, runLoopStop, getMain := sym("CFRunLoopRun"), sym("CFRunLoopStop"), sym("CFRunLoopGetMain")
	err := mainthread.Run(func() (mainthread.Call, error) {
		// The goroutine asks once the main thread runs its run loop.
		go func() {
			defer mainthread.RunOnMain(func() {
				main, _, _ := purego.SyscallN(getMain)
				mainthread.Forward[struct{}](runLoopStop, main)
			})
			for _, u := range drawUnits {
				before := mainthread.Queued()
				ok := u.draw()
				fmt.Printf("%s handed %d calls, drew %v\n", u.name, mainthread.Queued()-before, ok)
			}
		}()
		return mainthread.Call{Fn: runLoopRun}, nil
	}, func() {})
	if err != nil {
		return fail(err)
	}
	return 0
}

// While the worker runs, what a goroutine asks of AppKit is one step, the
// one call that it hands the main thread: AppKit's objects of one message
// would be gone for the next, were they forwarded one by one, and each
// message that runs on the main thread reaches it inside that step.
func TestAGoroutineDrawsInOneStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*testwait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), drawEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, u := range drawUnits {
		if w := u.name + " handed 1 calls, drew true\n"; !bytes.Contains(out, []byte(w)) {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
