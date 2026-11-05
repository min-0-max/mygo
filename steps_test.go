package mygo

import (
	"runtime"
	"testing"
	"time"
)

func TestRecordStepsRunsNoGoroutineOnceItStopped(t *testing.T) {
	before := runtime.NumGoroutine()
	stop := RecordSteps(func(StepRecord) {}, func(uint64) {})
	stop()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines after the recording stopped, %d before it", runtime.NumGoroutine(), before)
		}
		time.Sleep(time.Millisecond)
	}
}
