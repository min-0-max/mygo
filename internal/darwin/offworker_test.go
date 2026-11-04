//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/wait"
)

// offWorkerEnv makes the test binary the helper process of
// TestOffWorkerCallsLeaveTheMainThreadAlone.
const offWorkerEnv = "MYGO_DARWIN_TEST_OFFWORKER"

// offWorkerCall is a function that other goroutines call while the worker
// runs, with the forwarded calls that it makes.
type offWorkerCall struct {
	name      string
	forwarded int
	run       func(b *Backend, dir string) error
}

var offWorkerCalls = []offWorkerCall{
	{"Applications", 0, func(b *Backend, _ string) error {
		apps, err := b.Applications()
		if err != nil {
			return err
		}
		for _, a := range apps {
			if a.Name == "Finder" && a.ID == "com.apple.finder" {
				return nil
			}
		}
		return fmt.Errorf("no Finder in %d applications", len(apps))
	}},
	{"ReplaceFile", 0, func(b *Backend, dir string) error {
		target, replacement := filepath.Join(dir, "target"), filepath.Join(dir, "replacement")
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(replacement, []byte("new"), 0o644); err != nil {
			return err
		}
		if err := b.ReplaceFile(target, replacement); err != nil {
			return err
		}
		got, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if string(got) != "new" {
			return fmt.Errorf("the target holds %q", got)
		}
		// The replacement is gone: the system says why.
		err = b.ReplaceFile(target, replacement)
		if err == nil || strings.Contains(err.Error(), "cannot replace the file") {
			return fmt.Errorf("replacing with a file that is gone: %v", err)
		}
		return nil
	}},
	{"Package", 1, func(b *Backend, _ string) error {
		// The helper process is no bundle, which the path shows.
		if info, ok := b.App().Package(); ok {
			return fmt.Errorf("a package in a test binary: %+v", info)
		}
		return nil
	}},
	{"PreferredLanguages", 1, func(b *Backend, _ string) error {
		b.App().PreferredLanguages()
		return nil
	}},
	{"SavedLocale", 1, func(b *Backend, _ string) error {
		b.App().SavedLocale()
		return nil
	}},
	{"Region", 1, func(b *Backend, _ string) error {
		if r := b.App().Region(); r.Locale == "" {
			return fmt.Errorf("no locale in %+v", r)
		}
		return nil
	}},
	// One call for each Add, one for Remove, which the last Add follows in
	// the queue of the watch, and one for Close, however many streams it
	// stops.
	{"FileWatch", 6, func(b *Backend, dir string) error {
		w, err := b.NewFileWatch(func(platform.FileWatchNotice) {})
		if err != nil {
			return err
		}
		add := func(id uint64, name string) error {
			path := filepath.Join(dir, name)
			if err := os.Mkdir(path, 0700); err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			return w.Add(platform.FileWatchTarget{ID: id, Path: path, Directory: true, Info: info})
		}
		for id, name := range []string{"one", "two", "three"} {
			if err := add(uint64(id+1), name); err != nil {
				return err
			}
		}
		w.Remove(1)
		if err := add(4, "four"); err != nil {
			return err
		}
		w.Close()
		return nil
	}},
	// An object that ByReceiver would autorelease on the main thread
	// from here lives in a pool that this goroutine cannot see: it panics
	// here, not in the one forwarded call that may do it. That call, the
	// new and the release are what this case forwards.
	{"Autorelease", 3, func(*Backend, string) error {
		obj := send(class("NSObject"), "new")
		defer release(obj)
		mainthread.RunOnMain(func() {
			withPool(func() { autorelease(send(class("NSObject"), "new")) })
		})
		got := func() (p any) {
			defer func() { p = recover() }()
			autorelease(obj)
			return nil
		}()
		if s, ok := got.(string); !ok || !strings.Contains(s, "NSObject") {
			return fmt.Errorf("an autorelease forwarded off the worker: panic %v", got)
		}
		return nil
	}},
}

// drainAfterEachCall makes the method of cls that an off-worker call
// forwards one by one hold its result in a pool that the main thread
// drains as the method returns: the pool of the main run loop's pass may
// drain at any time after a forwarded call returned, and this is the
// first. A call made on another thread, or in the one forwarded call of a
// call made in one, keeps its result.
func drainAfterEachCall(cls, method string, classMethod bool, forwarded *atomic.Uint64) error {
	getMethod := "class_getInstanceMethod"
	if classMethod {
		getMethod = "class_getClassMethod"
	}
	sym := func(name string) (uintptr, error) { return purego.Dlsym(purego.RTLD_DEFAULT, name) }
	get, err := sym(getMethod)
	if err != nil {
		return err
	}
	setImp, err := sym("method_setImplementation")
	if err != nil {
		return err
	}
	onMain, err := sym("pthread_main_np")
	if err != nil {
		return err
	}
	m, _, _ := purego.SyscallN(get, uintptr(class(cls)), uintptr(sel(method)))
	if m == 0 {
		return fmt.Errorf("no method %s of %s", method, cls)
	}
	var orig uintptr
	imp := purego.NewCallback(func(self, cmd, a, b, c uintptr) uintptr {
		main, _, _ := purego.SyscallN(onMain)
		if main == 0 || mainthread.Forwarded()-forwarded.Load() < 2 {
			r, _, _ := purego.SyscallN(orig, self, cmd, a, b, c)
			return r
		}
		pool, _, _ := purego.SyscallN(poolPushFn)
		r, _, _ := purego.SyscallN(orig, self, cmd, a, b, c)
		purego.SyscallN(poolPopFn, pool)
		return r
	})
	orig, _, _ = purego.SyscallN(setImp, m, imp)
	return nil
}

// offWorkerMain is the helper process of
// TestOffWorkerCallsLeaveTheMainThreadAlone. On the main thread it starts
// the worker, and a step waits for another goroutine that makes each call
// of offWorkerCalls.
func offWorkerMain() int {
	fail := func(err error) int { fmt.Println(err); return 1 }
	b := New()
	if !mainthread.IsMainThread() {
		return fail(fmt.Errorf("the helper process does not run on the main thread"))
	}
	dir, err := os.MkdirTemp("", "offworker")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)
	var forwarded atomic.Uint64
	for _, m := range []struct {
		cls, method string
		classMethod bool
	}{
		{"NSFileManager", "displayNameAtPath:", false},
		{"NSURL", "fileURLWithPath:", true},
		{"NSBundle", "bundlePath", false},
		{"NSCalendar", "autoupdatingCurrentCalendar", true},
	} {
		if err := drainAfterEachCall(m.cls, m.method, m.classMethod, &forwarded); err != nil {
			return fail(err)
		}
	}
	err = mainthread.Run(func() error {
		for _, c := range offWorkerCalls {
			done := make(chan error)
			go func() {
				forwarded.Store(mainthread.Forwarded())
				err := c.run(b, dir)
				fmt.Printf("%s forwarded %d calls\n", c.name, mainthread.Forwarded()-forwarded.Load())
				done <- err
			}()
			if err := <-done; err != nil {
				return fmt.Errorf("%s: %w", c.name, err)
			}
		}
		// SaveLocale makes an array that it forwards to autorelease, on
		// the worker in a step, which may.
		before := mainthread.Forwarded()
		b.App().SaveLocale("x-test")
		b.App().SaveLocale("")
		if mainthread.Forwarded() == before {
			return fmt.Errorf("SaveLocale forwarded no call")
		}
		return nil
	}, func() {}, func() {})
	if err != nil {
		return fail(err)
	}
	return 0
}

// The functions that other goroutines call while the worker runs make the
// forwarded calls that offWorkerCalls name, and use nothing that they
// forwarded after the main thread's autorelease pool drained: zombies
// show an object used after that.
func TestOffWorkerCallsLeaveTheMainThreadAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), offWorkerEnv+"=1", "NSZombieEnabled=YES")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, c := range offWorkerCalls {
		if w := fmt.Sprintf("%s forwarded %d calls\n", c.name, c.forwarded); !strings.Contains(string(out), w) {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
