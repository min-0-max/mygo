//go:build darwin

package sfsymbols

import (
	"sort"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// The system's catalog of its symbols, in CoreGlyphs, the bundle AppKit
// draws them from: their names in the order the SF Symbols app shows
// them, and, should that list be missing, every name with the release
// that added it.
const (
	glyphs        = "/System/Library/CoreServices/CoreGlyphs.bundle/Contents/Resources/"
	orderList     = glyphs + "symbol_order.plist"
	availableList = glyphs + "name_availability.plist"
)

var names struct {
	once sync.Once
	list []string
}

// Names returns the names of the SF Symbols this macOS has, in the order
// the SF Symbols app shows them, as "0.circle" through "zzz", read from
// the system's own catalog; nil where there is none. The catalog is not
// public API: a macOS that moves it gives nil, while Image still draws the
// symbols it names.
func Names() []string {
	names.once.Do(func() {
		if !loadCG() {
			return
		}
		mainthread.RunOnMain(func() {
			withPool(func() {
				names.list = readOrder()
				if len(names.list) == 0 {
					names.list = readAvailable()
				}
			})
		})
	})
	return names.list
}

var (
	selInitFile  = objc.RegisterName("initWithContentsOfFile:")
	selInitArray = objc.RegisterName("initWithArray:")
	selObjectFor = objc.RegisterName("objectForKey:")
	selAllKeys   = objc.RegisterName("allKeys")
	selCount     = objc.RegisterName("count")
	selObjectAt  = objc.RegisterName("objectAtIndex:")
	selUTF8      = objc.RegisterName("UTF8String")
	selIsString  = objc.RegisterName("isKindOfClass:")
)

func readOrder() []string {
	path := nsString(orderList)
	defer release(path)
	arr := sendHere[objc.ID](sendHere[objc.ID](objc.ID(objc.GetClass("NSArray")), selAlloc), selInitFile, path)
	if arr == 0 {
		return nil
	}
	defer release(arr)
	return strings(arr)
}

func readAvailable() []string {
	path := nsString(availableList)
	defer release(path)
	dict := sendHere[objc.ID](sendHere[objc.ID](objc.ID(objc.GetClass("NSDictionary")), selAlloc), selInitFile, path)
	if dict == 0 {
		return nil
	}
	defer release(dict)
	key := nsString("symbols")
	defer release(key)
	symbols := sendHere[objc.ID](dict, selObjectFor, key)
	if symbols == 0 {
		return nil
	}
	// The file decides the class of symbols, so allKeys goes to the main
	// thread; the copy of its result is an array that this thread reads.
	keys := sendHere[objc.ID](sendHere[objc.ID](objc.ID(objc.GetClass("NSArray")), selAlloc), selInitArray,
		mainthread.Send[objc.ID](symbols, selAllKeys))
	defer release(keys)
	list := strings(keys)
	sort.Strings(list)
	return list
}

// strings returns the NSStrings of the NSArray arr, skipping anything
// else.
func strings(arr objc.ID) []string {
	if arr == 0 {
		return nil
	}
	class := objc.ID(objc.GetClass("NSString"))
	n := int(sendHere[uint](arr, selCount))
	out := make([]string, 0, n)
	for i := range n {
		s := sendHere[objc.ID](arr, selObjectAt, uint(i))
		if s == 0 || !sendHere[bool](s, selIsString, class) {
			continue
		}
		if p := sendHere[unsafe.Pointer](s, selUTF8); p != nil {
			out = append(out, goString(p))
		}
	}
	return out
}

// goString copies the C string at p.
func goString(p unsafe.Pointer) string {
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}
