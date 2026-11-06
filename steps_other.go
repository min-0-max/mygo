//go:build !darwin

package mygo

// RecordSteps has nothing to record on a platform whose main thread hands
// no steps to a worker.
func RecordSteps(report func(StepRecord), lost func(uint64)) (stop func()) { return func() {} }
