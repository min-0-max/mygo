//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/wait"
)

// resultsEnv makes the test binary the helper process of
// TestSurfaceResultsOutliveTheStep.
const resultsEnv = "MYGO_DARWIN_TEST_RESULTS"

// resultsMain is the helper process of TestSurfaceResultsOutliveTheStep,
// with zombies on, so that a deallocated object keeps a class that says
// so. On the main thread it makes a surface view, without a window, for a
// surface whose text input holds a text and whose accessibility has a
// top level, and starts the worker. In an autorelease pool of the main
// thread, it sends the view three messages that return objects, as AppKit
// does, which run on the worker as steps. It prints, for each result,
// whether it was deallocated once the step returned and once the main
// thread's pool drained.
func resultsMain() int {
	fail := func(err error) int { fmt.Println(err); return 1 }
	load()
	if !mainthread.IsMainThread() {
		return fail(fmt.Errorf("the helper process does not run on the main thread"))
	}
	var (
		poolPush     func() uintptr
		poolPop      func(pool uintptr)
		getClass     func(obj id) uintptr
		getClassName func(cls uintptr) string
	)
	purego.RegisterLibFunc(&poolPush, libObjC, "objc_autoreleasePoolPush")
	purego.RegisterLibFunc(&poolPop, libObjC, "objc_autoreleasePoolPop")
	purego.RegisterLibFunc(&getClass, libObjC, "object_getClass")
	purego.RegisterLibFunc(&getClassName, libObjC, "class_getName")
	deallocated := func(obj id) bool { return strings.HasPrefix(getClassName(getClass(obj)), "_NSZombie_") }

	b := New()
	theBackend = b
	registerSurfaceClass()
	view := msgInitRect(send(class("MyGoSurfaceView"), "alloc"), sel("initWithFrame:"), NSRect{Size: NSSize{Width: 100, Height: 100}})
	topLevel := send(send(class("NSMutableArray"), "alloc"), "initWithCapacity:", 1)
	send(topLevel, "addObject:", uintptr(view))
	s := &surface{
		w:        &window{b: b},
		view:     view,
		input:    platform.TextInputState{Text: "hello"},
		accessOn: true,
		topLevel: topLevel,
	}
	b.bySurface[view] = s

	if err := mainthread.Start(); err != nil {
		return fail(err)
	}
	type result struct {
		name           string
		obj            id
		afterStep      bool
		keptAfterDrain bool
	}
	pool := poolPush()
	var actual nsRange
	results := []result{
		{name: "validAttributesForMarkedText", obj: mainthread.Forward[id](msgSendAddr, view, sel("validAttributesForMarkedText"))},
		{name: "attributedSubstringForProposedRange:actualRange:", obj: mainthread.Forward[id](msgSendAddr, view, sel("attributedSubstringForProposedRange:actualRange:"), nsRange{Location: 0, Length: 5}, &actual)},
		{name: "accessibilityChildren", obj: mainthread.Forward[id](msgSendAddr, view, sel("accessibilityChildren")), keptAfterDrain: true},
	}
	for i := range results {
		results[i].afterStep = results[i].obj == 0 || deallocated(results[i].obj)
	}
	poolPop(pool)
	mainthread.Stop()
	for _, r := range results {
		fmt.Printf("%s: deallocated once the step returned: %v\n", r.name, r.afterStep)
		if r.keptAfterDrain {
			fmt.Printf("%s: deallocated once the pool drained: %v (the surface keeps it)\n", r.name, deallocated(r.obj))
		} else {
			fmt.Printf("%s: deallocated once the pool drained: %v\n", r.name, deallocated(r.obj))
		}
	}
	fmt.Println("accessibilityChildren is the top level:", results[2].obj == topLevel)
	return 0
}

// The objects that the surface view returns to AppKit from a step on the
// worker, the attributes of marked text, a substring and the
// accessibility children, outlive the step until the main thread's
// autorelease pool drains, and the top level, which the surface keeps,
// outlives that too. The view has no window.
func TestSurfaceResultsOutliveTheStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), resultsEnv+"=1", "NSZombieEnabled=YES")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"validAttributesForMarkedText: deallocated once the step returned: false",
		"validAttributesForMarkedText: deallocated once the pool drained: true",
		"attributedSubstringForProposedRange:actualRange:: deallocated once the step returned: false",
		"attributedSubstringForProposedRange:actualRange:: deallocated once the pool drained: true",
		"accessibilityChildren: deallocated once the step returned: false",
		"accessibilityChildren: deallocated once the pool drained: false (the surface keeps it)",
		"accessibilityChildren is the top level: true",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
