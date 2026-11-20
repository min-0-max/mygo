//go:build darwin

package mainthread_test

import (
	"testing"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// postingFunction is the function outside the framework that posts a closure
// through a function of the framework.
func postingFunction() string { return mainthread.SenderThroughFramework() }

// drainedSteps returns the steps that a drain reports, in its order.
func drainedSteps() []mainthread.Step {
	var steps []mainthread.Step
	mainthread.DrainSteps(func(step mainthread.Step) { steps = append(steps, step) })
	return steps
}

func TestAClosureIsRecordedWithTheFunctionThatPostedItAndItsRun(t *testing.T) {
	defer mainthread.UseFrameworkPrefix("github.com/egoist/mygo/internal/darwin/mainthread")()
	drainedSteps()
	mainthread.RecordSteps(true)
	defer mainthread.RecordSteps(false)
	sender := postingFunction()
	if want := "github.com/egoist/mygo/internal/darwin/mainthread_test.postingFunction"; sender != want {
		t.Fatalf("the sender is %q, want %q", sender, want)
	}
	ran := false
	mainthread.RunClosure(sender, func() { ran = true })
	if !ran {
		t.Fatal("the closure did not run")
	}
	steps := drainedSteps()
	if len(steps) != 1 {
		t.Fatalf("%d steps, want the closure: %+v", len(steps), steps)
	}
	if got := steps[0]; got.Kind != mainthread.StepClosure || got.Callback != sender || got.Entry == 0 || got.Return < got.Entry {
		t.Fatalf("the closure step is %+v", got)
	}
}

func TestAClosureIsNotRecordedWhileTheStepsAreNot(t *testing.T) {
	drainedSteps()
	if sender := postingFunction(); sender != "" {
		t.Fatalf("the sender is %q while the steps are not recorded", sender)
	}
	ran := false
	mainthread.RunClosure("", func() { ran = true })
	if steps := drainedSteps(); !ran || len(steps) != 0 {
		t.Fatalf("ran %v, steps %+v", ran, steps)
	}
}
