//go:build darwin

package darwin

import (
	"bytes"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
)

// TestCFDataBytes reads a secret's data as Secret does, with nothing of
// the keychain: a test that opens it may put a prompt on screen.
func TestCFDataBytes(t *testing.T) {
	load()
	create := mustDlsym(purego.RTLD_DEFAULT, "CFDataCreate")
	data := func(b []byte) uintptr {
		var p unsafe.Pointer
		if len(b) > 0 {
			p = unsafe.Pointer(&b[0])
		}
		r, _, _ := purego.SyscallN(create, 0, uintptr(p), uintptr(len(b)))
		if r == 0 {
			t.Fatal("CFDataCreate returned nothing")
		}
		return r
	}
	want := []byte{'s', 0, 'k'}
	d := data(want)
	got := cfDataBytes(d)
	cfRelease(d)
	if !bytes.Equal(got, want) {
		t.Errorf("cfDataBytes = %q, want %q", got, want)
	}
	e := data(nil)
	empty := cfDataBytes(e)
	cfRelease(e)
	if empty == nil || len(empty) != 0 {
		t.Errorf("cfDataBytes of empty data = %#v, want an empty slice", empty)
	}
}
