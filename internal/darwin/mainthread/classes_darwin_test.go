//go:build darwin

package mainthread

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

// Each method of a class that RegisterClass registers enters through a
// trampoline, and runs on the worker when the main thread calls it.
func TestRegisterClassSetsEachMethodToATrampoline(t *testing.T) {
	self := pthreadSelf(t)
	var ranOn uintptr
	answer := objc.RegisterName("mainthreadTestAnswer")
	c, err := RegisterClass("MainthreadTestClass", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{
		Cmd: answer,
		Fn: func(self_ objc.ID, cmd objc.SEL) uintptr {
			ranOn = direct[uintptr](self)
			return 42
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if imp := native.methodGetImplementation(native.classGetInstanceMethod(uintptr(c), answer)); !isTrampoline(imp) {
		t.Fatalf("the implementation %#x of the method is not a trampoline", imp)
	}
	obj := objc.ID(c).Send(objc.RegisterName("new"))
	defer obj.Send(objc.RegisterName("release"))
	onFakeMain(t, func() {
		var worker, got uintptr
		RunOnWorker(func() {
			worker = direct[uintptr](self)
			got = Send[uintptr](obj, answer)
		})
		if got != 42 || ranOn != worker {
			t.Errorf("the method answered %d on %#x, want 42 on the worker %#x", got, ranOn, worker)
		}
	})
}
