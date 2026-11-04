//go:build darwin

// Package mainthread holds what the main-thread worker of the darwin
// backend decides for each native call that Go code makes: which calls
// run on the calling thread, and which go to the main thread through the
// forwarding function or are decided by their receiver.
//
// Go code of the darwin backend runs on a worker thread, and the main
// thread runs the calls that the worker and other threads forward to it.
// A call runs on the calling thread only when this package says so: the
// messages and functions of CallingThreadReceivers and
// CallingThreadFunctions. The messages of ByReceiverSelectors and the
// functions of ByReceiverFunctions go through the function named
// ByReceiverFunc, which reads the class or the Core Foundation type of
// the receiver, runs the call on the calling thread for a receiver that
// CallingThreadReceivers or CFTypes name and forwards it for any other.
// Every other call goes through the function named ForwardFunc.
//
// The data lives here, in a package of its own under internal/darwin,
// because three parties read the same lists: the worker of
// internal/darwin, which forwards calls; the Metal renderer
// (internal/gpu/metal), the text engine (internal/text), netproxy and the
// other packages that make native calls on darwin, which import the
// worker's functions from here and cannot import internal/darwin, which
// imports them; and the tests that hold each call site and each call at
// run time to these lists. A list kept by one of them would let the other
// two disagree with it.
package mainthread

// The functions of this package that the call sites of mygo reach.
const (
	// ForwardFunc names the function that runs a native call on the main
	// thread, from the worker or any other thread, and returns its result.
	ForwardFunc = "Forward"
	// ByReceiverFunc names the function that runs a native call on the
	// calling thread or on the main thread by its receiver. Its first
	// argument is the native function, its second the receiver and its
	// third the selector of a message send.
	ByReceiverFunc = "ByReceiver"
)

// MessageSendFunctions are the functions of the Objective-C runtime whose
// call is a message send: their first argument is the receiver (for the
// super variants, a struct objc_super that holds it) and their second the
// selector. objc_msgSendSuper2 is the variant through which purego's objc
// package sends to the superclass.
var MessageSendFunctions = []string{
	"objc_msgSend",
	"objc_msgSend_stret",
	"objc_msgSendSuper",
	"objc_msgSendSuper2",
	"objc_msgSendSuper2_stret",
}

// ByReceiverSelectors are the messages that reach receivers of both kinds,
// those that run the message on the calling thread and those that the
// main thread must run, so that a call site does not decide them.
var ByReceiverSelectors = []string{"commit", "release", "autorelease"}

// ByReceiverFunctions are the functions that reach receivers of both kinds.
// _Block_copy and _Block_release run on the calling thread for a block
// that mygo made, whose dispose function is a trampoline of the worker.
var ByReceiverFunctions = []string{"CFRetain", "CFRelease", "_Block_copy", "_Block_release"}

// AutoreleasedCollectionPrefixes begin the selectors of the class methods
// that return an autoreleased array or dictionary (arrayWithObjects:,
// dictionary). Sent on the worker, they would put the collection in the
// worker's autorelease pool, whose drain at the end of a step would run its
// dealloc there, which releases the objects it holds; mygo makes each array
// and dictionary with alloc and an init message instead.
var AutoreleasedCollectionPrefixes = []string{"array", "dictionary"}

// Receivers is a group of receivers whose messages run on the calling
// thread.
type Receivers struct {
	Name string
	// AnyObject says that the Selectors run on the calling thread for any
	// receiver, as NSObject implements them.
	AnyObject bool
	// Protocols name the receivers by a protocol that they conform to.
	Protocols []string
	// Classes name the receivers by their class: instances of a class or
	// of a subclass of it.
	Classes []string
	// ClassMessages says that the messages to the classes of Classes, alloc
	// among them, run on the calling thread too.
	ClassMessages bool
	// AllMessages says that every message to the receivers runs on the
	// calling thread; otherwise only the Selectors do.
	AllMessages bool
	// Selectors are messages that mygo sends to the receivers and to no
	// other receiver. For a group whose AllMessages is false they are the
	// messages that run on the calling thread. A call site whose receiver
	// the reader of the call sites does not know is judged by its selector
	// against these lists, so a message is listed only when U1 names it
	// for the group or no class or protocol outside the group declares it
	// in the macOS SDK.
	Selectors []string
	// Decided are the messages of ByReceiverSelectors that ByReceiver runs
	// on the calling thread for the receivers.
	Decided []string
	// ReturnsMembers says that the objects that the messages to the
	// receivers return belong to the group, as the attachment descriptors
	// and the descriptor arrays that a Metal descriptor returns.
	ReturnsMembers bool
}

// CallingThreadReceivers are the receivers whose messages run on the
// calling thread.
var CallingThreadReceivers = []Receivers{
	{
		Name:      "any object",
		AnyObject: true,
		Selectors: []string{"retain", "respondsToSelector:", "isKindOfClass:"},
	},
	{
		Name: "Metal objects other than drawables",
		Protocols: []string{
			"MTLDevice", "MTLCommandQueue", "MTLCommandBuffer", "MTLCommandEncoder",
			"MTLRenderCommandEncoder", "MTLBlitCommandEncoder", "MTLComputeCommandEncoder",
			"MTLTexture", "MTLBuffer", "MTLLibrary", "MTLFunction",
			"MTLRenderPipelineState", "MTLComputePipelineState", "MTLSamplerState",
		},
		AllMessages: true,
		Selectors: []string{
			"GPUEndTime", "GPUStartTime", "blitCommandEncoder", "commandBuffer",
			"drawPrimitives:vertexStart:vertexCount:instanceCount:",
			"drawPrimitives:vertexStart:vertexCount:instanceCount:baseInstance:",
			"endEncoding", "functionConstantsDictionary",
			"getBytes:bytesPerRow:fromRegion:mipmapLevel:",
			"newBufferWithBytes:length:options:", "newBufferWithLength:options:",
			"newCommandQueue", "newFunctionWithName:", "newFunctionWithName:constantValues:error:",
			"newLibraryWithData:error:", "newLibraryWithSource:options:error:",
			"newRenderPipelineStateWithDescriptor:error:", "newSamplerStateWithDescriptor:",
			"newTextureWithDescriptor:", "presentDrawable:", "renderCommandEncoderWithDescriptor:",
			"replaceRegion:mipmapLevel:withBytes:bytesPerRow:",
			"setFragmentBuffer:offset:atIndex:", "setFragmentBytes:length:atIndex:",
			"setFragmentSamplerState:atIndex:", "setFragmentTexture:atIndex:",
			"setRenderPipelineState:", "setScissorRects:count:",
			"setVertexBuffer:offset:atIndex:", "setVertexBytes:length:atIndex:",
			"synchronizeResource:", "waitUntilCompleted", "waitUntilScheduled",
		},
		Decided: []string{"commit", "release", "autorelease"},
	},
	{
		Name: "Metal descriptors",
		Classes: []string{
			"MTLRenderPassDescriptor", "MTLRenderPipelineDescriptor",
			"MTLSamplerDescriptor", "MTLTextureDescriptor",
			"MTLRenderPassColorAttachmentDescriptor", "MTLRenderPassColorAttachmentDescriptorArray",
			"MTLRenderPassDepthAttachmentDescriptor", "MTLRenderPassStencilAttachmentDescriptor",
			"MTLRenderPipelineColorAttachmentDescriptor", "MTLRenderPipelineColorAttachmentDescriptorArray",
		},
		ClassMessages:  true,
		AllMessages:    true,
		ReturnsMembers: true,
		Selectors:      []string{},
		Decided:        []string{"release", "autorelease"},
	},
	{
		Name:      "drawables of a CAMetalLayer",
		Protocols: []string{"CAMetalDrawable"},
		Selectors: []string{"texture"},
		Decided:   []string{"release", "autorelease"},
	},
	{
		Name:      "CAMetalLayer",
		Classes:   []string{"CAMetalLayer"},
		Selectors: []string{"nextDrawable"},
	},
	{
		Name: "Foundation value objects that release on the calling thread",
		Classes: []string{
			"NSString", "NSMutableString", "NSAttributedString", "NSMutableAttributedString",
			"NSNumber", "NSData", "NSMutableData",
		},
		ClassMessages: true,
		AllMessages:   true,
		Selectors: []string{
			"UTF8String", "addAttribute:value:range:", "boolValue",
			"lengthOfBytesUsingEncoding:", "unsignedIntValue",
		},
		Decided: []string{"release", "autorelease"},
	},
	{
		Name:          "Foundation collections",
		Classes:       []string{"NSArray", "NSMutableArray", "NSDictionary", "NSMutableDictionary"},
		ClassMessages: true,
		AllMessages:   true,
		Selectors:     []string{},
	},
}

// CFTypes are the Core Foundation types for which ByReceiver runs CFRetain
// and CFRelease on the calling thread.
var CFTypes = []CFType{
	{Name: "CFString", Retain: true, Release: true},
	{Name: "CFAttributedString", Retain: true, Release: true},
	{Name: "CFNumber", Retain: true, Release: true},
	{Name: "CFData", Retain: true, Release: true},
	{Name: "CFArray", Retain: true, Release: true},
	{Name: "CFDictionary", Retain: true, Release: true},
	{Name: "CFSet", Retain: true, Release: true},
	{Name: "CTFont", Retain: true, Release: true},
	{Name: "CTFontDescriptor", Retain: true, Release: true},
	{Name: "CTLine", Retain: true, Release: true},
	{Name: "CTParagraphStyle", Retain: true, Release: true},
	{Name: "CTRunDelegate", Retain: true, Release: true},
	{Name: "CTTypesetter", Retain: true, Release: true},
	// mygo releases a run loop observer only after it added the observer
	// to the main run loop, which then holds it.
	{Name: "CFRunLoopObserver", Release: true},
}

// CFType is a Core Foundation type by the name of its type ID function
// (CFStringGetTypeID for CFString).
type CFType struct {
	Name            string
	Retain, Release bool
}

// FunctionGroup is a group of native functions that run on the calling
// thread: those named by Names, and those whose name begins with one of
// Prefixes and with none of Except.
type FunctionGroup struct {
	Name string
	// Package limits the group to the calls of one package of mygo, by its
	// import path below the module; empty, the group holds for every
	// package.
	Package  string
	Names    []string
	Prefixes []string
	Except   []string
}

// CallingThreadFunctions are the native functions that run on the calling
// thread. CFRetain and CFRelease are not among them: ByReceiver decides
// them.
var CallingThreadFunctions = []FunctionGroup{
	{Name: "Core Text", Prefixes: []string{"CT"}},
	{Name: "Core Graphics", Prefixes: []string{"CG"}},
	{
		Name:     "the Objective-C runtime other than its message sends",
		Prefixes: []string{"objc_", "sel_", "class_", "object_", "method_", "ivar_", "protocol_", "property_", "imp_"},
		Except:   []string{"objc_msgSend"},
	},
	{
		Name:  "Core Foundation functions of the value objects",
		Names: []string{"CFHash", "CFEqual"},
		Prefixes: []string{
			"CFString", "CFAttributedString", "CFNumber", "CFData",
			"CFArray", "CFDictionary", "CFSet",
		},
		Except: []string{"CFStringTokenizer", "CFNumberFormatter"},
	},
	{
		Name: "Core Foundation run loop functions",
		Names: []string{
			"CFRunLoopGetMain", "CFRunLoopObserverCreate", "CFRunLoopTimerCreate",
			"CFRunLoopAddTimer", "CFRunLoopAddSource", "CFRunLoopAddObserver",
			"CFRunLoopTimerSetNextFireDate", "CFRunLoopSourceSignal", "CFRunLoopWakeUp",
			"CFAbsoluteTimeGetCurrent", "CFGetTypeID",
		},
	},
	{
		Name:    "the proxy resolution of netproxy on a thread that it locks",
		Package: "netproxy",
		Names: []string{
			"CFNetworkCopySystemProxySettings", "CFNetworkCopyProxiesForURL",
			"CFNetworkExecuteProxyAutoConfigurationURL", "CFNetworkExecuteProxyAutoConfigurationScript",
			"CFRunLoopGetCurrent", "CFRunLoopAddSource", "CFRunLoopRunInMode",
			"CFRunLoopStop", "CFRunLoopRemoveSource", "CFRunLoopSourceInvalidate",
		},
	},
	{
		Name:    "the worker: the Mach semaphores with which the main thread and the worker wake each other, the run loop source through which other threads wake the main thread, the type of a run loop observer, which ByReceiver reads, and the assembly of this package that runs a record and hands a record to the other side",
		Package: "internal/darwin/mainthread",
		Names: []string{
			"semaphore_create", "semaphore_signal", "semaphore_wait",
			"CFRunLoopSourceCreate", "CFRunLoopObserverGetTypeID",
			"callRecord", "exchange",
		},
	},
	{Name: "libdispatch", Prefixes: []string{"dispatch_"}},
	{
		Name: "libSystem",
		Names: []string{
			"pthread_main_np", "pthread_set_qos_class_self_np",
			"proc_listallpids", "proc_pidpath", "proc_pid_rusage",
			"responsibility_get_pid_responsible_for_pid",
			"realpath", "realpath$DARWIN_EXTSN", "CACurrentMediaTime",
		},
	},
	{
		Name:  "the keychain",
		Names: []string{"SecItemAdd", "SecItemCopyMatching", "SecItemUpdate", "SecItemDelete"},
	},
}
