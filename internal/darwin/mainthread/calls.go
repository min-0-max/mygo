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

// CheckTag is the build tag that holds each call to these lists at run
// time. It builds a test of mygo, which the reader of the call sites does
// not read.
const CheckTag = "mainthreadcheck"

// The functions through which a call site binds a native function that
// runs on the calling thread, which the tag of CheckTag holds to these
// lists: BindFunc takes a library and a name as purego.RegisterLibFunc
// does, BindAddrFunc an address as purego.RegisterFunc does, and a name.
const (
	BindFunc     = "Bind"
	BindAddrFunc = "BindAddr"
)

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

// The functions of this package through which mygo makes the code that
// native code calls, so that it enters through a trampoline of the
// worker: a C callback that purego.NewCallback makes goes through
// CallbackFunc, or MethodFunc for a method implementation; a block that
// objc.NewBlock makes through BlockFunc; a method implementation that
// objc.NewIMP makes through MethodFunc; and a class is registered only
// through RegisterClassFunc, which sets each implementation of its
// methods to a trampoline. Each goes directly into its function.
const (
	CallbackFunc      = "Callback"
	BlockFunc         = "Block"
	MethodFunc        = "Method"
	RegisterClassFunc = "RegisterClass"
)

// SendFuncs name the functions that send a message through the function
// named ForwardFunc: their first argument is the receiver and their second
// the selector.
var SendFuncs = []string{"Send", "SendRetained"}

// RunOnMainFunc names the function that runs a Go function on the main
// thread as one forwarded call, inside which the calls that the function
// makes run at once.
const RunOnMainFunc = "RunOnMain"

// ForwardingFuncs name the functions of this package whose every call is
// a forwarded call: ForwardFunc, the function that takes the result of
// one on the main thread, the functions of SendFuncs and RunOnMainFunc.
var ForwardingFuncs = append([]string{ForwardFunc, "ForwardRetained", RunOnMainFunc}, SendFuncs...)

// OffWorkerFuncs are the functions that other goroutines call while the
// worker runs: those that platform.Backend lets any goroutine call, the
// methods that it lets any goroutine call of what they return, and the
// proxy resolution of netproxy. Outside the function that RunOnMainFunc
// runs, each of them, and each function that it reaches, makes no
// forwarded call but one call of RunOnMainFunc at most, and none in a
// loop. Forwarded one by one from another thread, two calls run in
// passes of the main run loop of their own, whose autorelease pool drains
// between them: an object that the first returns is gone when the second
// uses it.
var OffWorkerFuncs = []OffWorkerFunc{
	{"internal/darwin", "Backend.IsMainThread"},
	{"internal/darwin", "Backend.Signal"},
	{"internal/darwin", "Backend.Wake"},
	{"internal/darwin", "Backend.WebViewAvailable"},
	{"internal/darwin", "Backend.Secrets"},
	{"internal/darwin", "secrets.SetSecret"},
	{"internal/darwin", "secrets.Secret"},
	{"internal/darwin", "secrets.DeleteSecret"},
	{"internal/darwin", "Backend.Applications"},
	{"internal/darwin", "Backend.NewFileWatch"},
	{"internal/darwin", "fseWatch.Add"},
	{"internal/darwin", "fseWatch.Remove"},
	{"internal/darwin", "fseWatch.Close"},
	{"internal/darwin", "Backend.ReplaceFile"},
	{"internal/darwin", "appController.PreferredLanguages"},
	{"internal/darwin", "appController.Region"},
	{"internal/darwin", "appController.SavedLocale"},
	{"internal/darwin", "appController.Package"},
	{"netproxy", "system"},
}

// OffWorkerFunc is a function by its package, as an import path below the
// module, and its name, after the type of its receiver for a method
// (Backend.Wake).
type OffWorkerFunc struct{ Package, Name string }

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
	// Results are the messages whose result is a receiver of the group,
	// as the command buffer that commandBuffer returns.
	Results []Result
}

// Result is a message whose result is a receiver of a group. A message
// with no Of returns one to any receiver, so it is listed only when each
// class or protocol that declares it in the macOS SDK returns one; Of
// names the group of the receivers to which it returns one otherwise.
type Result struct {
	Selector string
	Of       string
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
		Results: []Result{
			{Selector: "newCommandQueue"}, {Selector: "commandBuffer"},
			{Selector: "renderCommandEncoderWithDescriptor:"}, {Selector: "blitCommandEncoder"},
			{Selector: "newBufferWithBytes:length:options:"}, {Selector: "newBufferWithLength:options:"},
			{Selector: "newTextureWithDescriptor:"},
			{Selector: "newLibraryWithData:error:"}, {Selector: "newLibraryWithSource:options:error:"},
			{Selector: "newFunctionWithName:"}, {Selector: "newFunctionWithName:constantValues:error:"},
			{Selector: "newRenderPipelineStateWithDescriptor:error:"}, {Selector: "newSamplerStateWithDescriptor:"},
			{Selector: "texture", Of: "drawables of a CAMetalLayer"},
		},
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
		Results:   []Result{{Selector: "nextDrawable", Of: "CAMetalLayer"}},
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
			"NSNumber", "NSData", "NSMutableData", "NSURL",
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
		// What they return is one of them or a value object.
		Name:           "Foundation classes that Apple documents as thread-safe",
		Classes:        []string{"NSBundle", "NSFileManager"},
		ClassMessages:  true,
		AllMessages:    true,
		ReturnsMembers: true,
		Selectors:      []string{},
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
	{Name: "CFURL", Retain: true, Release: true},
	{Name: "CTFont", Retain: true, Release: true},
	{Name: "CTFontDescriptor", Retain: true, Release: true},
	{Name: "CTLine", Retain: true, Release: true},
	{Name: "CTParagraphStyle", Retain: true, Release: true},
	{Name: "CTRunDelegate", Retain: true, Release: true},
	{Name: "CTTypesetter", Retain: true, Release: true},
	// mygo releases a run loop observer only after it added the observer
	// to the main run loop, which then holds it.
	{Name: "CFRunLoopObserver", Release: true},
	// netproxy releases a run loop source only after it invalidated the
	// source and removed it from the run loop of its own thread.
	{Name: "CFRunLoopSource", Release: true},
}

// CFType is a Core Foundation type by its name, which is the description
// of its type ID (CFCopyTypeIDDescription): CFString.
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

// CFValueTypes are the Core Foundation value objects, by the name of the
// type and the prefix of its functions.
var CFValueTypes = []string{
	"CFString", "CFAttributedString", "CFNumber", "CFData",
	"CFArray", "CFDictionary", "CFSet", "CFError",
}

// cfValueFunctions are the functions of CFValueTypes, which run on the
// calling thread for an object of one of those types: a run time check
// reads the type of the object that each of them gets.
var cfValueFunctions = FunctionGroup{
	Name:     "Core Foundation functions of the value objects",
	Names:    []string{"CFHash", "CFEqual"},
	Prefixes: CFValueTypes,
	Except:   []string{"CFStringTokenizer", "CFNumberFormatter"},
}

// CFCollectionTypes are the other types that a collection may hold at
// any depth for a CFRelease on the calling thread, with the Core Text
// types of CFTypes: the constants of CFBoolean are never deallocated, and
// a CFURL is immutable.
var CFCollectionTypes = []string{"CFBoolean", "CFURL"}

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
	cfValueFunctions,
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
			"CFURLCreateWithString",
		},
	},
	{
		Name:    "the worker: the Mach semaphores with which the main thread and the worker wake each other, the run loop source through which other threads wake the main thread, the description of a type ID, by which ByReceiver finds a type in CFTypes, and the assembly of this package that runs a record and hands a record to the other side",
		Package: "internal/darwin/mainthread",
		Names: []string{
			"semaphore_create", "semaphore_signal", "semaphore_wait",
			"CFRunLoopSourceCreate", "CFCopyTypeIDDescription",
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
		Names: []string{"SecItemAdd", "SecItemCopyMatching", "SecItemUpdate", "SecItemDelete", "SecCopyErrorMessageString"},
	},
}
