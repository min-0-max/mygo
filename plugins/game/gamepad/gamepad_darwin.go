package gamepad

import (
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// macOS reads pads through GameController.framework: GCController's
// controllers with an extended gamepad profile, which Xbox, PlayStation,
// Switch Pro and MFi controllers have. The framework finds controllers
// on the main run loop, which a running MyGo app turns. The messages to
// the controllers and to the list of them run on the main thread, which
// also takes the references that the reader keeps; the messages to the
// strings that a controller returns run on the calling thread.

var (
	gcOnce sync.Once
	gcOK   bool

	poolPush, poolPop, msgSend uintptr

	selControllers, selCount, selObjectAt, selExtended, selVendor, selUTF8,
	selValue, selXAxis, selYAxis, selRetain, selRelease, selResponds objc.SEL
	buttonSels                                                              [ButtonCount]objc.SEL
	selDpad, selLeftStick, selRightStick, selUp, selDown, selLeft, selRight objc.SEL
)

func loadGC() bool {
	gcOnce.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/GameController.framework/GameController", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
			return
		}
		libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		if poolPush, err = purego.Dlsym(libobjc, "objc_autoreleasePoolPush"); err != nil {
			return
		}
		if poolPop, err = purego.Dlsym(libobjc, "objc_autoreleasePoolPop"); err != nil {
			return
		}
		if msgSend, err = purego.Dlsym(libobjc, "objc_msgSend"); err != nil {
			return
		}
		sel := objc.RegisterName
		selControllers, selCount, selObjectAt = sel("controllers"), sel("count"), sel("objectAtIndex:")
		selExtended, selVendor, selUTF8 = sel("extendedGamepad"), sel("vendorName"), sel("UTF8String")
		selValue, selXAxis, selYAxis = sel("value"), sel("xAxis"), sel("yAxis")
		selRetain, selRelease, selResponds = sel("retain"), sel("release"), sel("respondsToSelector:")
		selDpad, selLeftStick, selRightStick = sel("dpad"), sel("leftThumbstick"), sel("rightThumbstick")
		selUp, selDown, selLeft, selRight = sel("up"), sel("down"), sel("left"), sel("right")
		for b, name := range map[Button]string{
			South: "buttonA", East: "buttonB", West: "buttonX", North: "buttonY",
			LeftShoulder: "leftShoulder", RightShoulder: "rightShoulder",
			LeftTrigger: "leftTrigger", RightTrigger: "rightTrigger",
			Back: "buttonOptions", Start: "buttonMenu", Guide: "buttonHome",
			LeftStickButton: "leftThumbstickButton", RightStickButton: "rightThumbstickButton",
		} {
			buttonSels[b] = sel(name)
		}
		gcOK = objc.GetClass("GCController") != 0
	})
	return gcOK
}

// withPool runs fn in an autorelease pool, for the objects the framework
// returns autoreleased. A pool belongs to its thread: the goroutine stays
// on one until the pool is popped.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, _, _ := purego.SyscallN(poolPush)
	defer purego.SyscallN(poolPop, p)
	fn()
}

type gcPad struct {
	ctl   objc.ID // retained
	title string
}

// controllers returns the controllers, which the list that it returns
// holds until release releases it.
func controllers() (ids []objc.ID, release func()) {
	list := mainthread.SendRetained(objc.ID(objc.GetClass("GCController")), selControllers)
	if list == 0 {
		return nil, func() {}
	}
	n := int(mainthread.Send[uint](list, selCount))
	ids = make([]objc.ID, n)
	for i := range n {
		ids[i] = mainthread.Send[objc.ID](list, selObjectAt, uint(i))
	}
	return ids, func() { mainthread.ByReceiver(msgSend, uintptr(list), uintptr(selRelease)) }
}

func scan(known map[string]device) []device {
	if !loadGC() {
		return nil
	}
	var found []device
	withPool(func() {
		list, release := controllers()
		defer release()
		for _, c := range list {
			if mainthread.Send[objc.ID](c, selExtended) == 0 {
				continue // a remote or a micro gamepad
			}
			k := strconv.FormatUint(uint64(c), 16)
			if d, ok := known[k]; ok {
				found = append(found, d)
				continue
			}
			mainthread.CheckSend(uintptr(c), uintptr(selRetain))
			d := &gcPad{ctl: c.Send(selRetain)}
			if v := mainthread.SendRetained(c, selVendor); v != 0 {
				mainthread.CheckSend(uintptr(v), uintptr(selUTF8))
				d.title = goString(objc.Send[uintptr](v, selUTF8))
				mainthread.ByReceiver(msgSend, uintptr(v), uintptr(selRelease))
			}
			found = append(found, d)
		}
	})
	return found
}

func goString(p uintptr) string {
	if p == 0 {
		return ""
	}
	b := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&p))), 256)
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func (d *gcPad) key() string  { return strconv.FormatUint(uint64(d.ctl), 16) }
func (d *gcPad) name() string { return d.title }
func (d *gcPad) close()       { mainthread.ByReceiver(msgSend, uintptr(d.ctl), uintptr(selRelease)) }

// gcPresent reports whether a controller is still among the controllers;
// tests replace it.
var gcPresent = func(ctl objc.ID) bool {
	list, release := controllers()
	defer release()
	for _, c := range list {
		if c == ctl {
			return true
		}
	}
	return false
}

func value(input objc.ID) float32 {
	if input == 0 {
		return 0
	}
	return mainthread.Send[float32](input, selValue)
}

func (d *gcPad) read(s *State) bool {
	ok := false
	withPool(func() {
		if ok = gcPresent(d.ctl); !ok {
			return
		}
		gp := mainthread.Send[objc.ID](d.ctl, selExtended)
		if gp == 0 {
			ok = false
			return
		}
		for b, sel := range buttonSels {
			// Older systems have no Home or Options button.
			if sel != 0 {
				mainthread.CheckSend(uintptr(gp), uintptr(selResponds))
			}
			if sel != 0 && objc.Send[bool](gp, selResponds, sel) {
				s.Buttons[b] = value(mainthread.Send[objc.ID](gp, sel))
			}
		}
		if pad := mainthread.Send[objc.ID](gp, selDpad); pad != 0 {
			s.Buttons[Up] = value(mainthread.Send[objc.ID](pad, selUp))
			s.Buttons[Down] = value(mainthread.Send[objc.ID](pad, selDown))
			s.Buttons[Left] = value(mainthread.Send[objc.ID](pad, selLeft))
			s.Buttons[Right] = value(mainthread.Send[objc.ID](pad, selRight))
		}
		// GameController's y axes go up.
		if st := mainthread.Send[objc.ID](gp, selLeftStick); st != 0 {
			s.Axes[LeftX], s.Axes[LeftY] = value(mainthread.Send[objc.ID](st, selXAxis)), -value(mainthread.Send[objc.ID](st, selYAxis))
		}
		if st := mainthread.Send[objc.ID](gp, selRightStick); st != 0 {
			s.Axes[RightX], s.Axes[RightY] = value(mainthread.Send[objc.ID](st, selXAxis)), -value(mainthread.Send[objc.ID](st, selYAxis))
		}
	})
	return ok
}
