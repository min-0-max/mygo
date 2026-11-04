//go:build darwin

package permissionflow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// macOS keeps the permissions in TCC, which the frameworks ask: the
// accessibility API (ApplicationServices), CoreGraphics for the screen and
// the input, AVFoundation for the microphone and the camera, EventKit for
// calendars and reminders, and CoreBluetooth. Their frameworks are loaded
// when a permission of theirs is first checked.
//
// Any goroutine calls this package while Go runs on the worker
// (internal/darwin/mainthread), so what asks the frameworks is one call on
// the main thread each (mainthread.RunOnMain), in a pool of that call, and
// passes only Go values on: the objects of two calls forwarded from another
// thread would live in pools of the main thread that drain between them.

const (
	applicationServicesPath = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	coreGraphicsPath        = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	coreFoundationPath      = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	avFoundationPath        = "/System/Library/Frameworks/AVFoundation.framework/AVFoundation"
	eventKitPath            = "/System/Library/Frameworks/EventKit.framework/EventKit"
	coreBluetoothPath       = "/System/Library/Frameworks/CoreBluetooth.framework/CoreBluetooth"
	appKitPath              = "/System/Library/Frameworks/AppKit.framework/AppKit"
	libObjCPath             = "/usr/lib/libobjc.A.dylib"

	settingsBundleID = "com.apple.systempreferences"

	// TCC's authorization statuses, as AVFoundation, EventKit and
	// CoreBluetooth number them.
	tccNotDetermined = 0
	tccRestricted    = 1
	tccDenied        = 2
	tccAuthorized    = 3

	ekEntityTypeEvent    = 0
	ekEntityTypeReminder = 1
)

var (
	coreOnce sync.Once
	coreErr  error

	// Called on the main thread by address.
	axIsProcessTrusted, axIsProcessTrustedWithOptions uintptr

	cgPreflightScreenCaptureAccess func() bool
	cgRequestScreenCaptureAccess   func() bool
	cgPreflightListenEventAccess   func() bool
	cgRequestListenEventAccess     func() bool
	cgWindowListCopyWindowInfo     func(option, relativeTo uint32) objc.ID
	cgEventSourceButtonState       func(state int32, button uint32) bool

	// CFRelease and _Block_release, which go through mainthread.ByReceiver.
	cfRelease, blockRelease uintptr

	// msgSend is objc_msgSend, and msgSendStret the entry point of the
	// messages that return a struct larger than 16 bytes, another one on
	// amd64.
	msgSend, msgSendStret uintptr

	poolPush, poolPop uintptr
	avMediaTypeAudio  objc.ID
	avMediaTypeVideo  objc.ID

	frameworks sync.Map // path → error of loading it
)

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// send sends a message on the main thread (mainthread.Send).
func send(obj objc.ID, name string, args ...any) objc.ID {
	return mainthread.Send[objc.ID](obj, sel(name), args...)
}

// sendRect sends a message that returns an nsRect on the main thread.
func sendRect(obj objc.ID, name string) nsRect {
	return mainthread.Forward[nsRect](msgSendStret, obj, sel(name))
}

// release releases obj through mainthread.ByReceiver.
func release(obj objc.ID) {
	mainthread.ByReceiver(msgSend, uintptr(obj), uintptr(sel("release")))
}

func loadCore() error {
	coreOnce.Do(func() {
		libs := map[string]uintptr{}
		for _, path := range []string{applicationServicesPath, coreGraphicsPath, coreFoundationPath, appKitPath, libObjCPath} {
			h, err := purego.Dlopen(path, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
			if err != nil {
				coreErr = fmt.Errorf("permissionflow: %w", err)
				return
			}
			libs[path] = h
		}
		sym := func(lib, name string) uintptr {
			p, err := purego.Dlsym(libs[lib], name)
			if err != nil && coreErr == nil {
				coreErr = fmt.Errorf("permissionflow: missing %s (macOS 10.15 and later): %w", name, errors.ErrUnsupported)
			}
			return p
		}
		bind := func(fn any, lib, name string) {
			if p := sym(lib, name); p != 0 {
				mainthread.BindAddr(fn, p, name)
			}
		}
		bind(&cgPreflightScreenCaptureAccess, coreGraphicsPath, "CGPreflightScreenCaptureAccess")
		bind(&cgRequestScreenCaptureAccess, coreGraphicsPath, "CGRequestScreenCaptureAccess")
		bind(&cgPreflightListenEventAccess, coreGraphicsPath, "CGPreflightListenEventAccess")
		bind(&cgRequestListenEventAccess, coreGraphicsPath, "CGRequestListenEventAccess")
		bind(&cgWindowListCopyWindowInfo, coreGraphicsPath, "CGWindowListCopyWindowInfo")
		bind(&cgEventSourceButtonState, coreGraphicsPath, "CGEventSourceButtonState")
		axIsProcessTrusted = sym(applicationServicesPath, "AXIsProcessTrusted")
		axIsProcessTrustedWithOptions = sym(applicationServicesPath, "AXIsProcessTrustedWithOptions")
		cfRelease = sym(coreFoundationPath, "CFRelease")
		blockRelease = sym(libObjCPath, "_Block_release")
		msgSend = sym(libObjCPath, "objc_msgSend")
		msgSendStret = msgSend
		if runtime.GOARCH == "amd64" {
			msgSendStret = sym(libObjCPath, "objc_msgSend_stret")
		}
		poolPush = sym(libObjCPath, "objc_autoreleasePoolPush")
		poolPop = sym(libObjCPath, "objc_autoreleasePoolPop")
	})
	return coreErr
}

// loadFramework loads the framework at path once, for its classes.
func loadFramework(path string) (uintptr, error) {
	type loaded struct {
		h   uintptr
		err error
	}
	if v, ok := frameworks.Load(path); ok {
		return v.(loaded).h, v.(loaded).err
	}
	h, err := purego.Dlopen(path, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		err = fmt.Errorf("permissionflow: %w", err)
	}
	v, _ := frameworks.LoadOrStore(path, loaded{h, err})
	return v.(loaded).h, v.(loaded).err
}

func loadAVFoundation() error {
	h, err := loadFramework(avFoundationPath)
	if err != nil {
		return err
	}
	if avMediaTypeAudio == 0 {
		audio, err1 := purego.Dlsym(h, "AVMediaTypeAudio")
		video, err2 := purego.Dlsym(h, "AVMediaTypeVideo")
		if err1 != nil || err2 != nil {
			return fmt.Errorf("permissionflow: AVFoundation lacks its media types: %w", errors.ErrUnsupported)
		}
		avMediaTypeAudio = **(**objc.ID)(unsafe.Pointer(&audio))
		avMediaTypeVideo = **(**objc.ID)(unsafe.Pointer(&video))
	}
	return nil
}

// withPool runs fn in an autorelease pool, on one thread.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, _, _ := purego.SyscallN(poolPush)
	defer purego.SyscallN(poolPop, p)
	fn()
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

// nsString returns an autoreleased NSString.
func nsString(s string) objc.ID {
	return send(class("NSString"), "stringWithUTF8String:", cstr(s))
}

func goString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := mainthread.Send[unsafe.Pointer](s, sel("UTF8String"))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

const ungated = true

// macMajor returns macOS's major version, 0 when it can't be read.
var macMajor = sync.OnceValue(func() int {
	if loadCore() != nil {
		return 0
	}
	type osVersion struct{ Major, Minor, Patch int }
	var v osVersion
	mainthread.RunOnMain(func() {
		withPool(func() {
			info := send(class("NSProcessInfo"), "processInfo")
			v = mainthread.Forward[osVersion](msgSendStret, info, sel("operatingSystemVersion"))
		})
	})
	return v.Major
})

func title(p Permission, t string) string {
	// macOS 27 renamed Accessibility; its anchor stays.
	if p == Accessibility && macMajor() >= 27 {
		return "Device Control and Data Access"
	}
	return t
}

// tccStatus converts a status of AVFoundation, EventKit or CoreBluetooth.
// EventKit's 4, write only, isn't the access asked for.
func tccStatus(v int) Status {
	switch v {
	case tccNotDetermined:
		return NotDetermined
	case tccAuthorized:
		return Granted
	}
	return Denied
}

func granted(ok bool) Status {
	if ok {
		return Granted
	}
	return Denied
}

func check(p Permission) (Status, error) {
	if err := loadCore(); err != nil {
		return Unknown, err
	}
	switch p {
	case Accessibility:
	case ScreenRecording:
		return granted(cgPreflightScreenCaptureAccess()), nil
	case InputMonitoring:
		return granted(cgPreflightListenEventAccess()), nil
	case FullDiskAccess:
		return fullDiskAccess(), nil
	case Microphone, Camera:
		if err := loadAVFoundation(); err != nil {
			return Unknown, err
		}
	case Calendars, Reminders:
		if _, err := loadFramework(eventKitPath); err != nil {
			return Unknown, err
		}
	case Bluetooth:
		if _, err := loadFramework(coreBluetoothPath); err != nil {
			return Unknown, err
		}
	default:
		return Unknown, nil
	}
	var st Status
	mainthread.RunOnMain(func() {
		withPool(func() { st = frameworkStatus(p) })
	})
	return st, nil
}

// frameworkStatus asks the framework of p for its status.
func frameworkStatus(p Permission) Status {
	switch p {
	case Accessibility:
		return granted(mainthread.Forward[bool](axIsProcessTrusted))
	case Microphone, Camera:
		return tccStatus(mainthread.Send[int](class("AVCaptureDevice"), sel("authorizationStatusForMediaType:"), mediaType(p)))
	case Calendars, Reminders:
		return tccStatus(mainthread.Send[int](class("EKEventStore"), sel("authorizationStatusForEntityType:"), entityType(p)))
	}
	cb := class("CBManager")
	if cb == 0 || !mainthread.Send[bool](cb, sel("respondsToSelector:"), sel("authorization")) {
		return Unknown
	}
	return tccStatus(mainthread.Send[int](cb, sel("authorization")))
}

func mediaType(p Permission) objc.ID {
	if p == Camera {
		return avMediaTypeVideo
	}
	return avMediaTypeAudio
}

func entityType(p Permission) int {
	if p == Reminders {
		return ekEntityTypeReminder
	}
	return ekEntityTypeEvent
}

// fullDiskAccess tries to read files that only apps with Full Disk Access
// read: TCC's own database of the user, then Safari's files.
func fullDiskAccess() Status {
	home, err := os.UserHomeDir()
	if err != nil {
		return Unknown
	}
	f, err := os.Open(filepath.Join(home, "Library/Application Support/com.apple.TCC/TCC.db"))
	if err == nil {
		f.Close()
		return Granted
	}
	if errors.Is(err, fs.ErrPermission) {
		return Denied
	}
	if _, err := os.ReadDir(filepath.Join(home, "Library/Safari")); err == nil {
		return Granted
	} else if errors.Is(err, fs.ErrPermission) {
		return Denied
	}
	return Unknown
}

// usageDescription checks that the app's Info.plist describes why it asks,
// as macOS ends apps in bundles that ask without. A program outside a
// bundle asks for the app that started it.
func usageDescription(keys ...string) error {
	var err error
	mainthread.RunOnMain(func() {
		withPool(func() {
			b := send(class("NSBundle"), "mainBundle")
			if !strings.HasSuffix(goString(send(b, "bundlePath")), ".app") {
				return
			}
			for _, k := range keys {
				if send(b, "objectForInfoDictionaryKey:", nsString(k)) == 0 {
					err = fmt.Errorf("%w: add %s to macos.infoPlist", ErrUsageDescription, k)
					return
				}
			}
		})
	})
	return err
}

var (
	eventStore     objc.ID // kept: a request dies with its store
	centralManager objc.ID // kept: Bluetooth asks while it lives
	requestMu      sync.Mutex
)

func request(ctx context.Context, p Permission) (Status, error) {
	st, err := check(p)
	if err != nil {
		return st, err
	}
	switch p {
	case Accessibility:
		if st == Granted {
			return st, nil
		}
		return promptAccessibility(), nil
	case ScreenRecording:
		if st == Granted {
			return st, nil
		}
		return granted(cgRequestScreenCaptureAccess()), nil
	case InputMonitoring:
		if st == Granted {
			return st, nil
		}
		return granted(cgRequestListenEventAccess()), nil
	}
	if st != NotDetermined {
		return st, nil
	}
	done := make(chan struct{}, 1)
	blk := mainthread.Block(objc.NewBlock(func(objc.Block, bool, objc.ID) {
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer mainthread.ByReceiver(blockRelease, uintptr(blk))
	switch p {
	case Microphone, Camera:
		key := "NSMicrophoneUsageDescription"
		if p == Camera {
			key = "NSCameraUsageDescription"
		}
		if err := usageDescription(key); err != nil {
			return st, err
		}
		askDevice(p, blk)
	case Calendars, Reminders:
		modern := fullEventAccess()
		keys := []string{"NSCalendarsUsageDescription"}
		if p == Reminders {
			keys = []string{"NSRemindersUsageDescription"}
		}
		if modern {
			keys[0] = strings.Replace(keys[0], "UsageDescription", "FullAccessUsageDescription", 1)
		}
		if err := usageDescription(keys...); err != nil {
			return st, err
		}
		askEvents(p, modern, blk)
	case Bluetooth:
		if err := usageDescription("NSBluetoothAlwaysUsageDescription"); err != nil {
			return st, err
		}
		startBluetooth()
		// CoreBluetooth calls no block: wait for the status to change.
		return waitDetermined(ctx, p)
	default:
		return st, nil
	}
	select {
	case <-done:
	case <-ctx.Done():
		return st, ctx.Err()
	}
	return check(p)
}

// promptAccessibility shows the system's prompt for Accessibility.
func promptAccessibility() (st Status) {
	mainthread.RunOnMain(func() {
		withPool(func() {
			opts := send(class("NSDictionary"), "dictionaryWithObject:forKey:",
				send(class("NSNumber"), "numberWithBool:", true), nsString("AXTrustedCheckOptionPrompt"))
			st = granted(mainthread.Forward[bool](axIsProcessTrustedWithOptions, opts))
		})
	})
	return st
}

// askDevice asks for the microphone or the camera.
func askDevice(p Permission, done objc.Block) {
	mainthread.RunOnMain(func() {
		withPool(func() {
			send(class("AVCaptureDevice"), "requestAccessForMediaType:completionHandler:", mediaType(p), done)
		})
	})
}

// fullEventAccess reports whether EventKit asks for full access.
func fullEventAccess() (full bool) {
	mainthread.RunOnMain(func() {
		full = mainthread.Send[bool](class("EKEventStore"), sel("instancesRespondToSelector:"), sel("requestFullAccessToEventsWithCompletion:"))
	})
	return full
}

// askEvents asks for calendars or reminders.
func askEvents(p Permission, full bool, done objc.Block) {
	mainthread.RunOnMain(func() {
		requestMu.Lock()
		defer requestMu.Unlock()
		if eventStore == 0 {
			eventStore = send(send(class("EKEventStore"), "alloc"), "init")
		}
		withPool(func() {
			switch {
			case full && p == Calendars:
				send(eventStore, "requestFullAccessToEventsWithCompletion:", done)
			case full:
				send(eventStore, "requestFullAccessToRemindersWithCompletion:", done)
			default:
				send(eventStore, "requestAccessToEntityType:completion:", entityType(p), done)
			}
		})
	})
}

// startBluetooth makes the central manager, which shows the prompt.
func startBluetooth() {
	mainthread.RunOnMain(func() {
		requestMu.Lock()
		defer requestMu.Unlock()
		if centralManager == 0 {
			// Its delegate would hear of the state of the radio:
			// the prompt shows without one.
			centralManager = send(send(class("CBCentralManager"), "alloc"), "initWithDelegate:queue:", objc.ID(0), objc.ID(0))
		}
	})
}

// waitDetermined waits until the user has answered for p.
func waitDetermined(ctx context.Context, p Permission) (Status, error) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		st, err := check(p)
		if err != nil || st != NotDetermined {
			return st, err
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-t.C:
		}
	}
}

func openSettings(p Permission) error {
	if err := loadCore(); err != nil {
		return err
	}
	ok := false
	mygo.RunOnMain(func() {
		withPool(func() {
			u := send(class("NSURL"), "URLWithString:", nsString(macSettingsURL(p)))
			ws := send(class("NSWorkspace"), "sharedWorkspace")
			ok = mainthread.Send[bool](ws, sel("openURL:"), u)
		})
	})
	if !ok {
		return fmt.Errorf("permissionflow: System Settings didn't open %s", macSettingsURL(p))
	}
	return nil
}
