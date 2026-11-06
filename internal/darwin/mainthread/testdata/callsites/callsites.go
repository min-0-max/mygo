// Package callsites holds one native call of each kind that the reader of
// the call sites judges. A line that the reader must reject ends with the
// message it must give; every other native call it must accept.
package callsites

import (
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	lib                                           uintptr
	msgSend, msgSendSuper, cfRelease, line, queue uintptr
	msgFloat                                      func(obj uintptr, s objc.SEL) float64
	superSend                                     func(sup uintptr, s objc.SEL, ev uintptr)
	fontSize                                      func(font uintptr) float64
	secAdd                                        func(query uintptr) int32
	queueStop                                     func(q uintptr, now bool) int32
	table                                         [2]func()
	hook                                          = dynamic
	selectors                                     = map[string]objc.SEL{}
	_                                             = unsafe.Pointer(nil)
)

// Forward runs a native call on the main thread.
func Forward(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

// Send sends a message on the main thread through Forward.
func Send(recv uintptr, s objc.SEL, args ...uintptr) uintptr {
	return Forward(msgSend, append([]uintptr{recv, uintptr(s)}, args...)...)
}

// Bind, BindAddr, Check and CheckSend are the functions through which a
// check at run time sees a native call on the calling thread.
func Bind(fn any, lib uintptr, name string)      { purego.RegisterLibFunc(fn, lib, name) }
func BindAddr(fn any, addr uintptr, name string) { purego.RegisterFunc(fn, addr) }
func Check(fn any, name string)                  {}
func CheckSend(recv, sel uintptr)                {}

// RunOnMain runs fn on the worker as one step.
func RunOnMain(fn func()) { fn() }

// ByReceiver runs a native call on the calling thread or the main thread
// by its receiver.
func ByReceiver(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

// Callback, Block and Method return the trampoline of a C callback, a
// block and a method implementation, and RegisterClass registers a class
// whose methods enter through trampolines.
func Callback(fn uintptr) uintptr                    { return fn }
func Block(b objc.Block) objc.Block                  { return b }
func Method(imp uintptr, returnsObject bool) uintptr { return imp }

func RegisterClass(name string, super objc.Class, protocols []*objc.Protocol, ivars []objc.FieldDef, methods []objc.MethodDef) (objc.Class, error) {
	return 0, nil
}

func sym(name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic(err)
	}
	return p
}

func load() {
	msgSend = sym("objc_msgSend")
	msgSendSuper = sym("objc_msgSendSuper")
	cfRelease = sym("CFRelease")
	line = sym("CTLineDraw")
	queue = sym("AudioQueueStart")
	purego.RegisterFunc(&msgFloat, msgSend)
	purego.RegisterFunc(&superSend, msgSendSuper)
	purego.RegisterLibFunc(&fontSize, lib, "CTFontGetSize")
	for _, f := range []struct {
		fn   any
		name string
	}{
		{&secAdd, "SecItemAdd"},
		{&queueStop, "AudioQueueStop"},
	} {
		p, err := purego.Dlsym(lib, f.name)
		if err != nil {
			return
		}
		purego.RegisterFunc(f.fn, p)
	}
}

func sel(name string) objc.SEL {
	if s, ok := selectors[name]; ok {
		return s
	}
	s := objc.RegisterName(name)
	selectors[name] = s
	return s
}

func class(name string) uintptr { return uintptr(objc.GetClass(name)) }

func send(obj uintptr, name string, args ...uintptr) uintptr {
	var a [8]uintptr
	a[0], a[1] = obj, uintptr(sel(name))
	n := copy(a[2:], args)
	r, _, _ := purego.SyscallN(msgSend, a[:n+2]...)
	return r
}

func alloc(name string) uintptr { return send(send(class(name), "alloc"), "init") }

func dynamic(name string) uintptr {
	return send(0, name) // want: a native call depends on parameters of dynamic and dynamic is used at callsites.go:22, where the reader cannot follow its arguments
}

func calls(obj, font, ctx uintptr) {
	purego.SyscallN(line, obj, ctx)
	purego.SyscallN(queue, obj) // want: calls AudioQueueStart outside Forward
	Forward(queue, obj)
	purego.SyscallN(cfRelease, obj) // want: calls CFRelease outside ByReceiver
	ByReceiver(cfRelease, obj)
	ByReceiver(queue, obj) // want: calls AudioQueueStart through ByReceiver, which decides only commit, release, autorelease and the functions that it names
	fontSize(font)
	secAdd(obj)
	queueStop(obj, true)                                    // want: calls AudioQueueStop outside Forward
	purego.SyscallN(uintptr(unsafe.Pointer(&obj)))          // want: calls a native function that no constant symbol binds: an expression the reader does not follow (*ast.UnaryExpr)
	purego.RegisterFunc(table[len(table)-int(obj)%2], line) // want: binds a function to a target that the reader cannot follow
}

func messages(obj, ev uintptr) {
	send(class("NSWindow"), "alloc") // want: sends alloc to the class NSWindow outside Forward
	send(class("NSString"), "stringWithUTF8String:", ev)
	alloc("NSMutableString")
	send(obj, "release") // want: sends release outside ByReceiver
	ByReceiver(msgSend, obj, uintptr(sel("release")))
	ByReceiver(msgSend, obj, uintptr(sel("close"))) // want: sends close through ByReceiver, which decides only commit, release, autorelease and the functions that it names
	send(obj, "retain")
	send(class("NSArray"), "arrayWithObjects:count:", ev, 1) // want: sends arrayWithObjects:count: outside Forward: a method that makes an autoreleased collection
	send(obj, "UTF8String")
	send(obj, "frame") // want: sends frame to a receiver outside Forward
	Forward(msgSend, obj, uintptr(sel("frame")))
	msgFloat(obj, sel("backingScaleFactor"))                          // want: sends backingScaleFactor to a receiver outside Forward
	superSend(obj, sel("mouseDown:"), ev)                             // want: sends mouseDown: to a receiver outside Forward
	objc.ID(obj).Send(sel("close"))                                   // want: sends close to a receiver outside Forward
	objc.Send[float64](objc.ID(obj), objc.RegisterName("alphaValue")) // want: sends alphaValue to a receiver outside Forward
	objc.ID(obj).SendSuper(sel("layout"))                             // want: sends layout to a receiver outside Forward
	objc.Block(ev).Release()                                          // want: calls _Block_release outside ByReceiver
	_ = hook
}

// probeAddress returns the address of an assembly function, probe.
func probeAddress() uintptr

func built() {
	purego.SyscallN(probeAddress()) // want: calls probe outside Forward
	for _, name := range []string{"CTFont", "AudioQueue"} {
		purego.SyscallN(sym(name + "GetTypeID")) // want: calls AudioQueueGetTypeID outside Forward
	}
}

func metal() {
	d := send(class("MTLRenderPipelineDescriptor"), "new")
	ca := send(send(d, "colorAttachments"), "objectAtIndexedSubscript:", 0)
	send(ca, "setPixelFormat:", 80)
	allocated := send(class("CAMetalLayer"), "alloc") // want: sends alloc to the class CAMetalLayer outside Forward
	layer := send(allocated, "init")                  // want: sends init to an instance of CAMetalLayer outside Forward
	send(layer, "setPixelFormat:", 80)                // want: sends setPixelFormat: to an instance of CAMetalLayer outside Forward
	drawable := send(layer, "nextDrawable")
	tex := send(drawable, "texture")
	send(tex, "width")
	send(drawable, "present") // want: sends present to one of the drawables of a CAMetalLayer outside Forward
	cb := send(send(device(), "newCommandQueue"), "commandBuffer")
	held := send(cb, "retain")
	send(held, "status")
	send(ByReceiver(msgSend, held, uintptr(sel("autorelease"))), "error")
	send(send(cb, "error"), "localizedDescription") // want: sends localizedDescription to a receiver outside Forward
	send(device(), "width")                         // want: sends width to a receiver outside Forward
	send(send(device(), "texture"), "width")        // want: sends width to a receiver outside Forward
}

// device returns an object whose class the reader does not know.
var device func() uintptr

// A holder holds objects in fields, which its methods assign.
type holder struct {
	cbs      [2]uintptr
	buf, obj uintptr
	layer    uintptr
}

func (h *holder) keep(cb uintptr) { h.cbs[0] = send(cb, "retain") }

func (h *holder) fields() {
	h.keep(send(send(device(), "newCommandQueue"), "commandBuffer"))
	send(h.cbs[0], "status")
	send(h.cbs[1], "status") // want: sends status to a receiver outside Forward
	b := &h.buf
	*b = send(device(), "newBufferWithLength:options:", 16, 0)
	send(*b, "contents")
	send(h.buf, "contents")
	h.obj = device()
	o := &h.obj
	send(*o, "contents") // want: sends contents to a receiver outside Forward
	h.layer = Send(Send(class("CAMetalLayer"), sel("alloc")), sel("init"))
	tex := send(send(h.layer, "nextDrawable"), "texture")
	send(tex, "width")
	send(Send(device(), sel("texture")), "width") // want: sends width to a receiver outside Forward
}

// buffer returns a command buffer, or nil.
func buffer(ok bool) uintptr {
	if !ok {
		return 0
	}
	return send(send(device(), "newCommandQueue"), "commandBuffer")
}

func zero(ok bool) {
	send(buffer(ok), "status")
	fn := uintptr(0)
	if ok {
		fn = cfRelease
	}
	purego.SyscallN(fn, 0) // want: calls a native function that no constant symbol binds: a constant that is not a string
}

func register() {
	objc.RegisterClass("CallSitesView", objc.GetClass("NSView"), nil, nil, []objc.MethodDef{
		{Cmd: sel("layout"), Fn: func(self objc.ID, cmd objc.SEL) {
			self.SendSuper(cmd) // want: sends layout to a receiver outside Forward
		}},
	})
}

// cache holds a view by key, which it makes the first time: the local that
// reads an element of views is what the element is assigned.
type cache struct{ views map[int]uintptr }

func (c *cache) show(key int) {
	v := c.views[key]
	if v == 0 {
		v = Send(class("NSView"), sel("new"))
		c.views[key] = v
	}
	send(v, "width") // want: sends width to a receiver outside Forward
}
