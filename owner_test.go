//go:build !js

package mygo

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// This owner and its subprocess inspect the desktop fake backend and helper
// dispatch installed by TestMain; browser tests have neither process ownership
// nor the application's native activation and notification delegate.
// The settings with which an application takes over what MyGo does for it
// by default reach the backend: none in the tests' process, which makes
// none of them, and each in that of ownedApp.
func TestOwnedAppOptions(t *testing.T) {
	if fb.AppOptions.NoSelfActivation {
		t.Errorf("AppOptions.NoSelfActivation without SetSelfActivation(false): true, want false")
	}
	if fb.AppOptions.NoNotificationDelegate {
		t.Errorf("AppOptions.NoNotificationDelegate without SetNotificationDelegate(false): true, want false")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "MYGO_TEST_OWNED_APP=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(out); !strings.Contains(got, "NoSelfActivation: true\n") ||
		!strings.Contains(got, "NoNotificationDelegate: true\n") {
		t.Errorf("output of SetSelfActivation(false) and SetNotificationDelegate(false):\n%s", got)
	}
}

// ownedApp is a helper process for TestOwnedAppOptions: it makes the
// settings that take over what MyGo does by default, runs the app, and
// prints the options that the backend received.
func ownedApp() {
	App.SetSelfActivation(false)
	App.SetNotificationDelegate(false)
	App.WhenReady(func() {
		fmt.Printf("NoSelfActivation: %v\n", fb.AppOptions.NoSelfActivation)
		fmt.Printf("NoNotificationDelegate: %v\n", fb.AppOptions.NoNotificationDelegate)
		os.Exit(0)
	})
	if err := App.Run(); err != nil {
		fmt.Println(err)
	}
	os.Exit(1)
}
