//go:build darwin

package text

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/wait"
)

// preloadEnv makes the test binary the helper process of
// TestPreloadKeepsTheFontPastTheMainPoolDrain.
const preloadEnv = "MYGO_TEXT_TEST_PRELOAD"

func TestMain(m *testing.M) {
	if os.Getenv(preloadEnv) == "1" {
		os.Exit(preloadMain())
	}
	os.Exit(m.Run())
}

// preloadMain is the helper process of
// TestPreloadKeepsTheFontPastTheMainPoolDrain, a process of its own so
// that no other engine holds the system font. A goroutine locked to its
// thread starts the worker as the main thread and pushes an autorelease
// pool; a step runs Preload on another goroutine, which associates an
// object with the font that Preload found; the main thread pops the pool
// after the step. Then the engine lets go of its own reference to the
// font and of its Font's. It prints whether Preload forwarded a call and
// when the font was deallocated.
func preloadMain() int {
	const associationRetain = 1 // OBJC_ASSOCIATION_RETAIN_NONATOMIC
	fail := func(err error) int { fmt.Println(err); return 1 }
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fail(err)
	}
	var (
		poolPush      func() uintptr
		poolPop       func(pool uintptr)
		setAssociated func(obj, key, value, policy uintptr)
	)
	purego.RegisterLibFunc(&poolPush, lib, "objc_autoreleasePoolPush")
	purego.RegisterLibFunc(&poolPop, lib, "objc_autoreleasePoolPop")
	purego.RegisterLibFunc(&setAssociated, lib, "objc_setAssociatedObject")
	var deallocs atomic.Int32
	sentinel, err := objc.RegisterClass("MyGoTestPreloadSentinel", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: objc.RegisterName("dealloc"), Fn: func(self objc.ID, cmd objc.SEL) {
			deallocs.Add(1)
			self.SendSuper(cmd)
		}},
	})
	if err != nil {
		return fail(err)
	}
	key := new(byte)

	// The engine loads AppKit before the worker starts, as App.Run does
	// when it makes the backend: AppKit loaded for the first time in a
	// step leaves NSFont unable to make the system font.
	s := newSystem()
	e, ok := s.engine().(*coreText)
	if !ok {
		return fail(fmt.Errorf("Core Text did not load"))
	}
	var font uintptr
	var forwarded uint64
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := mainthread.Start(); err != nil {
			done <- err
			return
		}
		defer mainthread.Stop()
		pool := poolPush()
		mainthread.RunOnWorker(func() {
			preloaded := make(chan struct{})
			go func() {
				defer close(preloaded)
				before := mainthread.Forwarded()
				s.Preload()
				forwarded = mainthread.Forwarded() - before
				s.mu.Lock()
				font = s.font(Style{}).native
				s.mu.Unlock()
				o := objc.ID(sentinel).Send(objc.RegisterName("new"))
				setAssociated(font, uintptr(unsafe.Pointer(key)), uintptr(o), associationRetain)
				o.Send(objc.RegisterName("release"))
			}()
			<-preloaded
		})
		poolPop(pool)
		done <- nil
	}()
	if err := <-done; err != nil {
		return fail(err)
	}
	runtime.KeepAlive(key)
	if font == 0 {
		return fail(fmt.Errorf("Preload found no font"))
	}
	fmt.Println("Preload forwarded a call:", forwarded > 0)
	fmt.Println("deallocated when the main thread's pool drained:", deallocs.Load() != 0)
	if deallocs.Load() != 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.forgetPrimary()
	fmt.Println("deallocated when the engine let go of its own reference:", deallocs.Load() != 0)
	if deallocs.Load() != 0 {
		// The Font would release the deallocated font once more.
		return 0
	}
	// As EndFrame lets go of the fonts, after which the Font releases its
	// reference once nothing refers to it (nativeFonts).
	s.clearLayouts()
	clear(s.marks)
	clear(s.fonts)
	clear(s.glyphs)
	clear(s.places)
	clear(s.runs)
	e.forgetFonts()
	wait.Poll(func() bool {
		runtime.GC()
		e.releaseFonts()
		return deallocs.Load() != 0
	})
	fmt.Println("deallocations when the engine let go of its Font's too:", deallocs.Load())
	return 0
}

// Preload, which a window runs on a goroutine of its own, finds the system
// font through NSFont, which makes it on the main thread and autoreleases
// it in the main thread's pool. Once that pool drained, the engine holds
// two references to the font, its own and its Font's: the font lives
// after the engine lets go of the first and is deallocated once after the
// Font, which holds the second, is gone.
func TestPreloadKeepsTheFontPastTheMainPoolDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), preloadEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"Preload forwarded a call: true",
		"deallocated when the main thread's pool drained: false",
		"deallocated when the engine let go of its own reference: false",
		"deallocations when the engine let go of its Font's too: 1",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
