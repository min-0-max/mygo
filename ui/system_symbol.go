package ui

import (
	"image"
	"image/color"
)

// Symbol is a symbol of the system: its mask, and its size in DIPs.
type Symbol struct {
	Bitmap *Bitmap
	W, H   float32
}

type symbolKey struct {
	name             string
	pointSize, scale float32
}

// SystemSymbol returns the symbol name of the system at pointSize, for the
// scale of the window, and a Symbol without a Bitmap when the system has no
// such symbol, as only macOS has a library of them. Draw it with
// Painter.Symbol. The window asks the system once for each size and scale.
func (c *context) SystemSymbol(name string, pointSize float32) Symbol {
	rt := c.rt
	_, _, scale := rt.host.size()
	key := symbolKey{name, pointSize, scale}
	if symbol, ok := rt.symbols[key]; ok {
		return symbol
	}
	var symbol Symbol
	if mask, ok := rt.host.systemSymbol(name, float64(pointSize), float64(scale)); ok {
		symbol = symbolOfMask(mask, scale)
	}
	if rt.symbols == nil {
		rt.symbols = map[symbolKey]Symbol{}
	}
	rt.symbols[key] = symbol
	return symbol
}

// symbolOfMask makes the bitmap that a tint colors: white, with the alpha of mask.
func symbolOfMask(mask image.Image, scale float32) Symbol {
	b := mask.Bounds()
	white := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			_, _, _, a := mask.At(b.Min.X+x, b.Min.Y+y).RGBA()
			white.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: uint8(a >> 8)})
		}
	}
	return Symbol{Bitmap: NewBitmap(white), W: float32(b.Dx()) / scale, H: float32(b.Dy()) / scale}
}

// Symbol draws a symbol of the system in the middle of box, in color c. A
// symbol has its own size, which its point size chose.
func (p *Painter) Symbol(s Symbol, box Rect, c Color) {
	if s.Bitmap == nil {
		return
	}
	dst := Rect{X: box.X + (box.W-s.W)/2, Y: box.Y + (box.H-s.H)/2, W: s.W, H: s.H}
	p.ImageRegion(s.Bitmap, Rect{}, dst, ImageOptions{Tint: c})
}
