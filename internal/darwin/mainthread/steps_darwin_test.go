//go:build darwin

package mainthread

import (
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/callbacks"
)

// stepNanos reads the clock of the step records.
func stepNanos() uint64 { return direct[uint64](link.clock, uintptr(stepClock)) }

// recordingSteps has the steps of fn recorded and returns them in the order of their returns.
func recordingSteps(t *testing.T, fn func()) []Step {
	t.Helper()
	var steps []Step
	drain := func() { DrainSteps(func(step Step) { steps = append(steps, step) }) }
	drain()
	steps = nil
	RecordSteps(true)
	defer RecordSteps(false)
	fn()
	drain()
	return steps
}

func TestAStepReportsItsEntryReturnForwardedCallsAndWait(t *testing.T) {
	self := pthreadSelf(t)
	const slept = 3 * time.Millisecond
	step := Callback(purego.NewCallback(func() uintptr {
		Forward[uintptr](self)
		time.Sleep(slept)
		Forward[uintptr](self)
		return 0
	}))
	onFakeMain(t, func() {
		var before, after uint64
		steps := recordingSteps(t, func() {
			before = stepNanos()
			direct[uintptr](step)
			after = stepNanos()
		})
		if len(steps) != 1 {
			t.Fatalf("%d steps, want 1: %+v", len(steps), steps)
		}
		got := steps[0]
		if got.Kind != StepCallback || !strings.Contains(got.Callback, "TestAStepReports") {
			t.Fatalf("the step is %+v", got)
		}
		if got.Entry < before || got.Return < got.Entry || got.Return > after {
			t.Fatalf("the step %+v ran outside %d to %d", got, before, after)
		}
		if got.Forwarded != 2 {
			t.Fatalf("the step forwarded %d calls, want 2", got.Forwarded)
		}
		if got.Wait < uint64(slept) || got.Wait > got.Return-got.Entry {
			t.Fatalf("the main thread waited %d of %d ns, want at least %d", got.Wait, got.Return-got.Entry, slept)
		}
	})
}

func TestANestedStepRunsInsideTheStepThatForwardedItsCaller(t *testing.T) {
	self := pthreadSelf(t)
	inner := Callback(purego.NewCallback(func() uintptr {
		Forward[uintptr](self)
		Forward[uintptr](self)
		return 0
	}))
	outer := Callback(purego.NewCallback(func() uintptr { return Forward[uintptr](inner) }))
	onFakeMain(t, func() {
		steps := recordingSteps(t, func() { direct[uintptr](outer) })
		if len(steps) != 2 {
			t.Fatalf("%d steps, want 2: %+v", len(steps), steps)
		}
		in, out := steps[0], steps[1]
		if in.Entry < out.Entry || in.Return > out.Return {
			t.Fatalf("the nested step %+v is not inside %+v", in, out)
		}
		if in.Forwarded != 2 || out.Forwarded != 1 {
			t.Fatalf("forwarded %d inside and %d outside, want 2 and 1", in.Forwarded, out.Forwarded)
		}
		// The call that the outer step forwarded holds the inner step, so the inner step is not a wait of the outer.
		if out.Wait > out.Return-out.Entry-(in.Return-in.Entry) {
			t.Fatalf("the outer step waited %d of %d ns, with %d ns in its forwarded call", out.Wait, out.Return-out.Entry, in.Return-in.Entry)
		}
	})
}

func TestAMethodReportsItsSelectorAndABlockItsKind(t *testing.T) {
	sel := objc.RegisterName("stepRecordProbe:")
	class, err := RegisterClass("StepRecordProbe", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel, Fn: func(objc.ID, objc.SEL, uintptr) uintptr { return 7 }},
	})
	if err != nil {
		t.Fatal(err)
	}
	object := objc.ID(class).Send(objc.RegisterName("new"))
	defer object.Send(objc.RegisterName("release"))
	block := Block(objc.NewBlock(func(objc.Block) uintptr { return 9 }))
	invoke := *word(uintptr(block) + blockInvoke)
	imp := native.methodGetImplementation(native.classGetInstanceMethod(uintptr(class), sel))
	onFakeMain(t, func() {
		steps := recordingSteps(t, func() {
			if got := direct[uintptr](imp, uintptr(object), uintptr(sel), uintptr(1)); got != 7 {
				t.Errorf("the method returned %d", got)
			}
			if got := direct[uintptr](invoke, uintptr(block)); got != 9 {
				t.Errorf("the block returned %d", got)
			}
		})
		if len(steps) != 2 {
			t.Fatalf("%d steps, want 2: %+v", len(steps), steps)
		}
		if steps[0].Kind != StepSelector || steps[0].Callback != "stepRecordProbe:" {
			t.Fatalf("the method reports %+v", steps[0])
		}
		if steps[1].Kind != StepBlock || !strings.Contains(steps[1].Callback, "TestAMethodReports") {
			t.Fatalf("the block reports %+v", steps[1])
		}
	})
}

func TestAStepOutsideARecordingTouchesNoRing(t *testing.T) {
	step := Callback(purego.NewCallback(func() uintptr { return 0 }))
	onFakeMain(t, func() {
		DrainSteps(func(Step) {})
		before := stepRing.head
		for range 100 {
			direct[uintptr](step)
		}
		if stepRing.head != before {
			t.Fatalf("100 steps outside a recording moved the ring from %d to %d", before, stepRing.head)
		}
		if allocs := testing.AllocsPerRun(100, func() { DrainSteps(func(Step) {}) }); allocs != 0 {
			t.Fatalf("a drain of an empty ring allocates %v", allocs)
		}
	})
}

func TestARingThatOverflowsReportsTheLostSteps(t *testing.T) {
	step := Callback(purego.NewCallback(func() uintptr { return 0 }))
	onFakeMain(t, func() {
		DrainSteps(func(Step) {})
		RecordSteps(true)
		defer RecordSteps(false)
		for range stepSlots + 10 {
			direct[uintptr](step)
		}
		count := 0
		lost := DrainSteps(func(Step) { count++ })
		if lost != 10 || count != stepSlots {
			t.Fatalf("lost %d and reported %d steps, want 10 and %d", lost, count, stepSlots)
		}
	})
}

func TestRegisteredCallbacksCountEachCallbackOnce(t *testing.T) {
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	before := callbacks.Count()
	made := purego.NewCallback(func() uintptr { return 0 })
	Callback(made)
	Callback(made)
	if got := callbacks.Count(); got != before+1 {
		t.Fatalf("one callback took the count from %d to %d", before, got)
	}
	methods := []objc.MethodDef{
		{Cmd: objc.RegisterName("stepCountFirst"), Fn: func(objc.ID, objc.SEL) uintptr { return 0 }},
		{Cmd: objc.RegisterName("stepCountSecond"), Fn: func(objc.ID, objc.SEL) uintptr { return 0 }},
	}
	if _, err := RegisterClass("StepCountProbe", objc.GetClass("NSObject"), nil, nil, methods); err != nil {
		t.Fatal(err)
	}
	if got := callbacks.Count(); got != before+3 {
		t.Fatalf("a class of two methods took the count to %d, want %d", got, before+3)
	}
	Block(objc.NewBlock(func(objc.Block, uint32) uintptr { return 0 }))
	if got := callbacks.Count(); got != before+4 {
		t.Fatalf("the first block of a type took the count to %d, want %d", got, before+4)
	}
	Block(objc.NewBlock(func(objc.Block, uint32) uintptr { return 1 }))
	if got := callbacks.Count(); got != before+4 {
		t.Fatalf("a second block of the type took the count to %d, want %d", got, before+4)
	}
}
