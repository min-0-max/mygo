//go:build darwin

package mainthread

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// ByReceiver runs a call on the calling thread for the receivers that
// CallingThreadReceivers and CFTypes name and for the blocks that Block
// set up, and forwards it for any other receiver.
func TestByReceiverDecidesACallByItsReceiver(t *testing.T) {
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	d := &decisions
	cf := "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cstr := func(s string) *byte { b := append([]byte(s), 0); return &b[0] }
	str := direct[uintptr](dlsym(t, cf, "CFStringCreateWithCString"), uintptr(0), cstr("x"), uint32(0x08000100))
	date := direct[uintptr](dlsym(t, cf, "CFDateCreate"), uintptr(0), 0.0)
	device := direct[uintptr](dlsym(t, "/System/Library/Frameworks/Metal.framework/Metal", "MTLCreateSystemDefaultDevice"))
	nsObject := objc.ID(objc.GetClass("NSObject")).Send(objc.RegisterName("new"))
	release := uintptr(objc.RegisterName("release"))
	closeSel := uintptr(objc.RegisterName("close"))
	made := Block(objc.NewBlock(func(objc.Block) {}))
	// A block that the system made: its descriptor holds a dispose
	// function of the system.
	descriptor := [5]uintptr{0, 32, dlsym(t, cf, "CFRelease"), dlsym(t, cf, "CFRelease"), 0}
	system := [4]uintptr{0, blockHasCopyDispose, 0, uintptr(unsafe.Pointer(&descriptor))}
	cases := []struct {
		name string
		fn   uintptr
		args []uintptr
		want bool
	}{
		{"CFRelease of a CFString", d.cfRelease, []uintptr{str}, true},
		{"CFRetain of a CFString", d.cfRetain, []uintptr{str}, true},
		{"CFRelease of a CFDate", d.cfRelease, []uintptr{date}, false},
		{"release of an NSString", d.msgSend, []uintptr{str, release}, true},
		{"release of an NSObject", d.msgSend, []uintptr{uintptr(nsObject), release}, false},
		{"release of a class", d.msgSend, []uintptr{uintptr(objc.GetClass("NSString")), release}, false},
		{"close, which ByReceiver does not decide", d.msgSend, []uintptr{str, closeSel}, false},
		{"_Block_release of a block that Block set up", d.blockRelease, []uintptr{uintptr(made)}, true},
		{"_Block_release of a block that the system made", d.blockRelease, []uintptr{uintptr(unsafe.Pointer(&system))}, false},
	}
	if device != 0 {
		cases = append(cases, struct {
			name string
			fn   uintptr
			args []uintptr
			want bool
		}{"release of a Metal device", d.msgSend, []uintptr{device, release}, true})
	}
	for _, c := range cases {
		if got := callingThread(c.fn, c.args); got != c.want {
			t.Errorf("%s: on the calling thread = %v, want %v", c.name, got, c.want)
		}
	}
	runtime.KeepAlive(&system)
	runtime.KeepAlive(&descriptor)
}

// A call that ByReceiver decided once for the type or the class of its
// receiver allocates nothing when it comes again: the text engine releases
// its Core Foundation objects at the end of a frame, and a window that
// shows nothing new still draws frames.
func TestARepeatedDecisionAllocatesNothing(t *testing.T) {
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	d := &decisions
	cf := "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cstr := func(s string) *byte { b := append([]byte(s), 0); return &b[0] }
	str := direct[uintptr](dlsym(t, cf, "CFStringCreateWithCString"), uintptr(0), cstr("x"), uint32(0x08000100))
	release := uintptr(objc.RegisterName("release"))
	cases := []struct {
		name string
		call func()
	}{
		{"CFRetain and CFRelease of a CFString", func() {
			ByReceiver(d.cfRetain, str)
			ByReceiver(d.cfRelease, str)
		}},
		{"CFRetain and the release message of an NSString", func() {
			ByReceiver(d.cfRetain, str)
			ByReceiver(d.msgSend, str, release)
		}},
	}
	for _, c := range cases {
		c.call()
		if n := testing.AllocsPerRun(100, c.call); n != 0 {
			t.Errorf("%s allocates %v times", c.name, n)
		}
	}
	ByReceiver(d.cfRelease, str)
}

// A block that Block set up runs on the worker when the main thread
// calls it.
func TestABlockThatTheMainThreadCallsRunsOnTheWorker(t *testing.T) {
	self := pthreadSelf(t)
	var ranOn uintptr
	b := Block(objc.NewBlock(func(objc.Block) { ranOn = direct[uintptr](self) }))
	invoke := *word(uintptr(b) + blockInvoke)
	if !isTrampoline(invoke) {
		t.Fatal("Block did not set the invoke function to a trampoline")
	}
	onFakeMain(t, func() {
		var worker uintptr
		RunOnWorker(func() {
			worker = direct[uintptr](self)
			Forward[uintptr](invoke, uintptr(b))
		})
		if ranOn != worker {
			t.Errorf("the block ran on %#x, the worker is %#x", ranOn, worker)
		}
	})
}
