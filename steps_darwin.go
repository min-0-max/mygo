//go:build darwin

package mygo

import (
	"time"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// stepDrainInterval is how often RecordSteps reports what the main thread
// recorded. The ring of the main thread holds 4096 steps.
const stepDrainInterval = 100 * time.Millisecond

// RecordSteps has the main thread record its steps until stop returns.
// report and lost run on a goroutine of RecordSteps: report with each step
// in the order of the returns, lost with how many steps the main thread
// could not record. A last report runs before stop returns. Without a
// recording a step reads one word more and no goroutine runs.
func RecordSteps(report func(StepRecord), lost func(uint64)) (stop func()) {
	drain := func() {
		missed := mainthread.DrainSteps(func(step mainthread.Step) {
			report(StepRecord{
				Kind: StepKind(step.Kind), Callback: step.Callback, Entry: step.Entry, Return: step.Return,
				Forwarded: step.Forwarded, Wait: step.Wait,
			})
		})
		if missed != 0 {
			lost(missed)
		}
	}
	mainthread.RecordSteps(true)
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(stepDrainInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				drain()
			case <-quit:
				mainthread.RecordSteps(false)
				drain()
				return
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}
