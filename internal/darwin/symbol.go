//go:build darwin

package darwin

import (
	"image"
	"math"
	"unsafe"
)

// SystemSymbol draws the symbol name of the system at pointSize, in the
// regular weight, into a bitmap of the pixels of a window of scale and returns
// its alpha. The system gives a symbol no path to read, only an image to draw.
func (t theme) SystemSymbol(name string, pointSize, scale float64) (image.Image, bool) {
	var mask *image.Alpha
	withPool(func() {
		const symbolNamed = "imageWithSystemSymbolName:accessibilityDescription:"
		images := class("NSImage")
		if !respondsTo(images, symbolNamed) {
			return // before macOS 11
		}
		img := send(images, symbolNamed, uintptr(nsString(name)), 0)
		if img == 0 {
			return
		}
		const regular = 0 // NSFontWeightRegular
		config := msgFloat2ID(class("NSImageSymbolConfiguration"), sel("configurationWithPointSize:weight:"), pointSize, regular)
		img = send(img, "imageWithSymbolConfiguration:", uintptr(config))
		if img == 0 {
			return
		}
		size := msgSize(img, sel("size"))
		w, h := int(math.Ceil(size.Width*scale)), int(math.Ceil(size.Height*scale))
		if w <= 0 || h <= 0 {
			return
		}
		rep := autorelease(msgInitBitmap(send(class("NSBitmapImageRep"), "alloc"),
			sel("initWithBitmapDataPlanes:pixelsWide:pixelsHigh:bitsPerSample:samplesPerPixel:hasAlpha:isPlanar:colorSpaceName:bytesPerRow:bitsPerPixel:"),
			0, w, h, 8, 4, true, false, nsString("NSCalibratedRGBColorSpace"), 0, 0))
		if rep == 0 {
			return
		}
		msgSetSize(rep, sel("setSize:"), size)
		ctx := send(class("NSGraphicsContext"), "graphicsContextWithBitmapImageRep:", uintptr(rep))
		if ctx == 0 {
			return
		}
		gc := class("NSGraphicsContext")
		send(gc, "saveGraphicsState")
		send(gc, "setCurrentContext:", uintptr(ctx))
		const sourceOver = 2 // NSCompositingOperationSourceOver
		msgDrawImage(img, sel("drawInRect:fromRect:operation:fraction:"), NSRect{Size: size}, NSRect{}, sourceOver, 1)
		send(gc, "restoreGraphicsState")
		data, stride := send(rep, "bitmapData"), sendInt(rep, "bytesPerRow")
		if data == 0 {
			return
		}
		pixels := unsafe.Slice(*(**byte)(unsafe.Pointer(&data)), stride*h)
		mask = image.NewAlpha(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				mask.Pix[mask.PixOffset(x, y)] = pixels[y*stride+x*4+3]
			}
		}
	})
	return mask, mask != nil
}
