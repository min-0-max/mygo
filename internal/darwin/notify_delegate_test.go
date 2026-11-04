//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/wait"
)

// notifyDelegateEnv makes the test binary the helper process of
// TestNotificationDelegate; notifyOwnedEnv=1 sets
// AppOptions.NoNotificationDelegate in it.
const (
	notifyDelegateEnv = "MYGO_DARWIN_TEST_NOTIFY_DELEGATE"
	notifyOwnedEnv    = "MYGO_DARWIN_TEST_NOTIFY_OWNED"
)

// notifyDelegateMain is the helper process of TestNotificationDelegate,
// which runs it from an application bundle, where the notification center
// exists. It starts the backend with the option of the application, and
// prints what the center's delegate is then: MyGo's, none or another. The
// policy keeps the helper out of the Dock.
func notifyDelegateMain() int {
	load()
	b := New()
	if !b.NotificationsSupported() {
		fmt.Println("the helper process has no notification center: it does not run from an application bundle with UNUserNotificationCenter")
		return 1
	}
	err := b.Init(notifyHandler{}, platform.AppOptions{
		ActivationPolicy:       "prohibited",
		NoNotificationDelegate: os.Getenv(notifyOwnedEnv) == "1",
	})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	var delegate id
	withPool(func() { delegate = send(notificationCenter(), "delegate") })
	switch {
	case delegate == 0:
		fmt.Println("delegate: none")
	case delegate == b.notifyDelegate:
		fmt.Println("delegate: MyGo's")
	default:
		fmt.Println("delegate: another")
	}
	return 0
}

// notifyHandler answers no callback: the helper runs no event loop, so
// the backend calls none of them.
type notifyHandler struct{ platform.AppHandler }

// notifyBundle returns the executable of an application bundle that holds
// the test binary, through a hard link, so that the binary runs as an
// application of its own identifier, as a packaged app does.
func notifyBundle(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents := filepath.Join(t.TempDir(), "MyGoNotifyDelegate.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>dev.mygo.test.notify-delegate</string>
	<key>CFBundleExecutable</key>
	<string>MyGoNotifyDelegate</string>
	<key>CFBundleName</key>
	<string>MyGoNotifyDelegate</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(contents, "MacOS", "MyGoNotifyDelegate")
	if err := os.Link(exe, bundled); err != nil {
		t.Fatalf("the test binary cannot be linked into the bundle: %v", err)
	}
	return bundled
}

// The backend sets the delegate of the notification center as it starts,
// unless AppOptions.NoNotificationDelegate leaves the delegate to the
// application (U6).
func TestNotificationDelegate(t *testing.T) {
	exe := notifyBundle(t)
	for _, c := range []struct {
		name, owned, want string
	}{
		{"by default", "0", "delegate: MyGo's"},
		{"owned by the application", "1", "delegate: none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^$")
			cmd.Env = append(os.Environ(), notifyDelegateEnv+"=1", notifyOwnedEnv+"="+c.owned)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v:\n%s", err, out)
			}
			if !strings.Contains(string(out), c.want+"\n") {
				t.Errorf("no %q in the output:\n%s", c.want, out)
			}
		})
	}
}
