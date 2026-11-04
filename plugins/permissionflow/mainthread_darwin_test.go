//go:build darwin

package permissionflow

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	testwait "github.com/egoist/mygo/internal/wait"
)

// What a goroutine asks the frameworks while the worker runs is one call
// on the main thread each, none of which shows anything: forwarded one by
// one, the objects of one message would be gone for the next.
func TestAGoroutineAsksInOneMainCall(t *testing.T) {
	if err := loadCore(); err != nil {
		t.Fatal(err)
	}
	units := []struct {
		name string
		ask  func()
	}{
		{"Check of Accessibility", func() { check(Accessibility) }},
		{"Check of the microphone", func() { check(Microphone) }},
		{"the usage description", func() { usageDescription("NSMicrophoneUsageDescription") }},
		{"the process of System Settings", func() { settingsProcess() }},
		{"the frame of System Settings", func() { settingsFrame(int32(os.Getpid())) }},
		{"the app", func() { defaultApp() }},
	}
	forwarded := make([]uint64, len(units))
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := mainthread.Run(func() error {
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				for i, u := range units {
					before := mainthread.Forwarded()
					u.ask()
					forwarded[i] = mainthread.Forwarded() - before
				}
			}()
			select {
			case <-finished:
			case <-time.After(testwait.Guard):
				t.Errorf("the goroutine never finished (waited %v)", testwait.Guard)
			}
			return nil
		}, func() {}, func() {})
		if err != nil {
			t.Error(err)
		}
	}()
	<-done
	for i, u := range units {
		if forwarded[i] != 1 {
			t.Errorf("%s forwarded %d calls to the main thread, want 1", u.name, forwarded[i])
		}
	}
}

// A struct larger than 16 bytes returns through another entry point of
// the messages on amd64.
func TestStructMessages(t *testing.T) {
	if err := loadCore(); err != nil {
		t.Fatal(err)
	}
	want := nsRect{nsPoint{1, 2}, nsSize{3, 4}}
	withPool(func() {
		if got := sendRect(send(class("NSValue"), "valueWithRect:", want), "rectValue"); got != want {
			t.Errorf("rectValue = %+v, want %+v", got, want)
		}
	})
	if macMajor() < 10 {
		t.Errorf("macOS %d", macMajor())
	}
}
