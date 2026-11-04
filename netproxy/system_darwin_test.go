//go:build darwin && (amd64 || arm64)

package netproxy

import (
	"net/url"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/wait"
)

// TestSystemDarwin asks CFNetwork for the proxy of a URL: whatever the
// machine's settings, the answer is a proxy Go can use or none.
func TestSystemDarwin(t *testing.T) {
	if cf() == nil {
		t.Fatal("CFNetwork did not load")
	}
	p := system(&url.URL{Scheme: "https", Host: "example.com", Path: "/"})
	if p != nil && p.Host == "" {
		t.Errorf("system = %v, a proxy without a host", p)
	}
	t.Logf("system proxy: %v", p)
}

// TestPACScript runs a setup script on this thread's run loop and reads
// the proxies it chose.
func TestPACScript(t *testing.T) {
	a := cf()
	if a == nil {
		t.Fatal("CFNetwork did not load")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	script := a.stringCreate(0, `function FindProxyForURL(url, host) { return "PROXY p.example:8080; SOCKS s.example:1080; DIRECT"; }`, cfStringEncodingUTF8)
	defer a.releaseCF(script)
	s := a.stringCreate(0, "https://example.com/", cfStringEncodingUTF8)
	defer a.releaseCF(s)
	target := mainthread.Forward[uintptr](a.cfURLCreate, uintptr(0), s, uintptr(0))
	defer a.releaseCF(target)
	got := a.runPAC(script, target, true)
	want := []string{"http://p.example:8080", "socks5://s.example:1080", "direct://"}
	if !slices.Equal(got, want) {
		t.Errorf("runPAC = %q, want %q", got, want)
	}
}

// TestPACScriptCallbackRunsOnItsGoroutine runs a setup script as system
// does, on a goroutine of an application whose Go code runs on the
// worker: the goroutine's thread runs the script on its run loop, the
// callback of the script runs on that thread, neither on the worker nor
// on the main thread, and the calls that the goroutine forwards meanwhile
// run on the main thread.
func TestPACScriptCallbackRunsOnItsGoroutine(t *testing.T) {
	a := cf()
	if a == nil {
		t.Fatal("CFNetwork did not load")
	}
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	pthreadSelf, err := purego.Dlsym(lib, "pthread_self")
	if err != nil {
		t.Fatal(err)
	}
	self := func() uintptr { r, _, _ := purego.SyscallN(pthreadSelf); return r }

	var callbackOn uintptr
	var callbackMain bool
	saved := a.callback
	a.callback = mainthread.Callback(purego.NewCallback(func(info, list, cfErr uintptr) {
		callbackOn, callbackMain = self(), mainthread.IsMainThread()
		pacDone(info, list, cfErr)
	}))
	defer func() { a.callback = saved }()

	var main, worker, goroutine uintptr
	var got []string
	var forwarded uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		main = self()
		err := mainthread.Run(func() error {
			worker = self()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				goroutine = self()
				before := mainthread.Forwarded()
				script := a.stringCreate(0, `function FindProxyForURL(url, host) { return "PROXY p.example:8080; DIRECT"; }`, cfStringEncodingUTF8)
				defer a.releaseCF(script)
				s := a.stringCreate(0, "https://example.com/", cfStringEncodingUTF8)
				defer a.releaseCF(s)
				target := mainthread.Forward[uintptr](a.cfURLCreate, uintptr(0), s, uintptr(0))
				defer a.releaseCF(target)
				got = a.runPAC(script, target, true)
				forwarded = mainthread.Forwarded() - before
			}()
			select {
			case <-finished:
			case <-time.After(wait.Guard):
				t.Errorf("the setup script on the goroutine never finished (waited %v)", wait.Guard)
			}
			return nil
		}, func() {}, func() {})
		if err != nil {
			t.Error(err)
		}
	}()
	<-done

	if want := []string{"http://p.example:8080", "direct://"}; !slices.Equal(got, want) {
		t.Errorf("runPAC = %q, want %q", got, want)
	}
	if callbackOn != goroutine || callbackMain {
		t.Errorf("the callback ran on %#x (IsMainThread %v), not on the goroutine's thread %#x (main %#x, worker %#x)", callbackOn, callbackMain, goroutine, main, worker)
	}
	if forwarded == 0 {
		t.Error("the goroutine forwarded no call to the main thread")
	}
}
