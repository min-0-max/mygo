//go:build darwin

package mygo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/fake"
	"github.com/egoist/mygo/internal/platform"
)

func init() { workerTestMain = workerMain }

// workerBackend is the fake backend with the worker of the main thread,
// as the darwin backend has it: Run runs the Go code of App.Run on the
// worker around an event loop on the main thread, which hands each
// dispatch to the worker through a trampoline, and Step, which await
// calls on the worker, waits for an event on the main thread through a
// forwarded call.
type workerBackend struct {
	*fake.Backend
	h                     platform.AppHandler
	signal, wake, quit    chan struct{}
	quitOnce              sync.Once
	ready, dispatch, wait uintptr
	mainThread            uintptr
}

var pthreadSelf = sync.OnceValue(func() uintptr {
	p, err := purego.Dlsym(purego.RTLD_DEFAULT, "pthread_self")
	if err != nil {
		panic(err)
	}
	return p
})

func thread() uintptr { r, _, _ := purego.SyscallN(pthreadSelf()); return r }

func newWorkerBackend() *workerBackend {
	b := &workerBackend{Backend: fake.New(), signal: make(chan struct{}, 1), wake: make(chan struct{}, 1), quit: make(chan struct{})}
	b.ready = mainthread.Callback(purego.NewCallback(func() { b.h.Ready() }))
	b.dispatch = mainthread.Callback(purego.NewCallback(func() { b.h.Dispatch() }))
	// The wait of the main thread for one event, which Step forwards.
	b.wait = purego.NewCallback(func() {
		select {
		case <-b.signal:
			purego.SyscallN(b.dispatch)
		case <-b.wake:
		case <-b.quit:
		}
	})
	return b
}

func (b *workerBackend) Init(h platform.AppHandler, opts platform.AppOptions) error {
	b.h = h
	return b.Backend.Init(h, opts)
}

func (b *workerBackend) Run(start func() error, finish func()) error {
	return mainthread.Run(start, func() {
		b.mainThread = thread()
		purego.SyscallN(b.ready)
		for {
			select {
			case <-b.quit:
				return
			case <-b.signal:
				purego.SyscallN(b.dispatch)
			case <-b.wake:
			}
		}
	}, finish)
}

func (b *workerBackend) IsMainThread() bool { return mainthread.IsMainThread() }

func (b *workerBackend) Signal() {
	select {
	case b.signal <- struct{}{}:
	default:
	}
}

func (b *workerBackend) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *workerBackend) Step() { mainthread.Forward[struct{}](b.wait) }

func (b *workerBackend) Quit() { b.quitOnce.Do(func() { close(b.quit) }) }

// workerMain is a helper process for TestTheMainThreadIsTheWorker: it
// runs the app on workerBackend and prints where onMain, RunOnMain and
// await ran.
func workerMain() {
	b := newWorkerBackend()
	backendOnce.Do(func() { theBackend = b })
	stopGrace = 0
	App.OnWindowAllClosed(func() {})
	App.WhenReady(func() {
		where := func(name string) {
			fmt.Printf("%s: main thread %v, worker %v\n", name, isMainThread(), thread() != b.mainThread)
		}
		where("ready")
		go func() {
			onMain(func() { where("onMain") })
			RunOnMain(func() { where("RunOnMain") })
			v := onMainValue(func() int {
				ch := make(chan int, 1)
				go func() {
					// Work for the main thread, which await runs while it
					// waits, then the value.
					onMain(func() { where("during await") })
					deliver(ch, 7)
				}()
				return await(ch)
			})
			fmt.Println("await:", v)
			App.Quit()
		}()
	})
	App.OnQuit(func() { fmt.Println("quit on the worker:", isMainThread() && thread() != b.mainThread) })
	if err := App.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	// The worker ended: the main goroutine is back on the main thread alone.
	fmt.Println("after Run on the main thread:", thread() == b.mainThread && mainthread.IsMainThread())
	os.Exit(0)
}

// TestTheMainThreadIsTheWorker: with the worker of the main thread, the
// Go code of the app that runs on the main thread runs on the worker:
// listeners, onMain and RunOnMain from other goroutines, and await, which
// keeps running the work of the main thread through Backend.Step while it
// waits.
func TestTheMainThreadIsTheWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "MYGO_TEST_WORKER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	want := []string{
		"ready: main thread true, worker true",
		"onMain: main thread true, worker true",
		"RunOnMain: main thread true, worker true",
		"during await: main thread true, worker true",
		"await: 7",
		"quit on the worker: true",
		"after Run on the main thread: true",
	}
	got := string(out)
	for _, w := range want {
		if !strings.Contains(got, w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, got)
		}
	}
}
