package text

import (
	"image"
	"math"

	"github.com/egoist/mygo/internal/scene"
)

// A Shade is how light the color of text is, from 0 (dark) to Shades-1
// (light). Core Text's font smoothing, which AppKit draws text with,
// emboldens glyphs more the lighter their color is, in four steps of its
// relative luminance: the glyphs of its fonts are rasterized for each.
type Shade uint8

// Shades is the number of shades of text.
const Shades = 4

// Thick is a shade beyond the others, for text drawn thicker: with Core
// Text's font smoothing at its strongest, whatever the text's color and
// the user's setting, as Ghostty's font-thicken draws it. Engines that do
// not thicken glyphs draw them as for other text.
const Thick Shade = Shades

// Flat is a shade beyond the others, for text drawn without font
// smoothing, whatever its color: thinner, as browsers draw text styled
// -webkit-font-smoothing: antialiased on macOS. Engines that do not
// smooth fonts draw it as other text.
const Flat Shade = Shades + 1

// shadeFor returns the shade that glyphs of f for text of shade are drawn
// for.
func (f *Font) shadeFor(shade Shade) Shade {
	switch {
	case shade == Thick && f.thickens:
		return Thick
	case !f.shaded:
		return 0
	case shade == Flat:
		return Flat
	}
	return min(shade, Shades-1)
}

// ShadeOf returns the shade of text of an sRGB color: its relative
// luminance rounded to a quarter, as Core Text rounds it, and at most 3/4.
func ShadeOf(r, g, b uint8) Shade {
	y := 0.2126*linear[r] + 0.7152*linear[g] + 0.0722*linear[b]
	return Shade(min(y*Shades+0.5, Shades-1))
}

// linear maps sRGB values to linear light.
var linear = func() (t [256]float32) {
	for i := range t {
		v := float64(i) / 255
		if v <= 0.04045 {
			v /= 12.92
		} else {
			v = math.Pow((v+0.055)/1.055, 2.4)
		}
		t[i] = float32(v)
	}
	return t
}()

type glyphKey struct {
	font     *Font
	id       uint32
	scale    uint32 // pixels per DIP, in 1/256
	subX     uint8  // the position within a pixel, of placement.n
	shade    Shade
	subpixel bool
	// stroke is the width of a stroke of the glyph's outline in 1/16
	// pixels (StrokeGlyph), 0 for the glyph itself; shadow is 1 + the
	// standard deviation of the blur of its shadow in 1/16 pixels
	// (ShadowGlyph), 0 for none.
	stroke uint16
	shadow uint16
}

// placement is how the system's text stack places glyphs of a font at a
// scale horizontally: at n positions within a pixel, the nearest to their
// pen if round, else the one left of it.
type placement struct {
	n     int
	round bool
}

type placeKey struct {
	font     *Font
	scale    uint32
	subpixel bool
}

// GlyphImage is a rasterized glyph in an atlas.
type GlyphImage struct {
	// OK is false for glyphs without pixels, such as spaces.
	OK bool
	// Colored glyphs are in the color atlas, the others in the mask atlas
	// but Subpixel ones, in the color atlas too, which hold the coverage of
	// each pixel's red, green and blue subpixels.
	Colored, Subpixel bool
	// Thin glyphs are of a font too thin for antialiasing, which renderers
	// give more contrast (scene.Glyph.Thin).
	Thin bool
	// X, Y, W and H are the bitmap's rectangle in its atlas.
	X, Y, W, H uint16
	// Left and Top place the bitmap relative to the glyph's origin on the
	// baseline, in pixels: Top is usually negative.
	Left, Top float32
}

// inColor reports whether the glyph is in the color atlas.
func (g GlyphImage) inColor() bool { return g.Colored || g.Subpixel }

// atlasEntry is a cached glyph or mask.
type atlasEntry struct {
	GlyphImage
	used uint64 // the last frame that drew it
}

// BeginFrame starts painting a frame: the masks drawn for the previous
// frame alone are forgotten.
func (s *System) BeginFrame() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MaskAtlas.ResetTransient()
	clear(s.transient)
	s.forgetLeftOut()
	s.rooms = 0
}

// forgetLeftOut forgets what the atlases left out, for a new frame or as they
// have room.
func (s *System) forgetLeftOut() {
	s.full, s.want = [2]bool{}, [2]int{}
	s.wantSize = [2]image.Point{}
	s.reviewing = false
	clear(s.outGlyphs)
	clear(s.outRuns)
	clear(s.outMasks)
}

// TransientUses returns how many times Mask returned a mask drawn for the
// frame being painted alone, since the system started: a scene drawing one
// must not be drawn again after the next frame of any window begins
// (BeginFrame), which draws over it.
func (s *System) TransientUses() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transientUses
}

// Full reports whether an atlas filled up during the frame being painted
// and left out glyphs or masks it draws. MakeRoom then makes room for them.
func (s *System) Full() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.full[0] || s.full[1]
}

// Atlas review: nothing but a full atlas evicts, and an atlas never fills
// again after what made it large is gone. So a frame painted in full
// reviews the atlases as it ends: the last before the window settles,
// which no other will follow, and among frames that keep moving one in
// atlasReviewFrames.
const (
	atlasReviewFrames = 120
	// atlasLiveFrames is how recently a window that takes turns with others
	// drew what it still shows.
	atlasLiveFrames = 8
	// An atlas with a sixteenth of it in use halves. What it holds then
	// takes at most a quarter, which leaves it room before it must grow.
	atlasShrinkFill = 16
	atlasSmallest   = 256
)

// touch notes that the frame draws e.
func (s *System) touch(e *atlasEntry) {
	if e.used == s.frame {
		return
	}
	e.used = s.frame
	s.draws(e.inColor(), e.W, e.H)
}

// draws adds a w×h bitmap to the room the frame draws from an atlas.
func (s *System) draws(color bool, w, h uint16) {
	i := 0
	if color {
		i = 1
	}
	s.drawn[i] += (int(w) + 1) * (int(h) + 1)
}

// Review closes a frame window painted in full, which has drawn what the
// window shows and is the last before it settles when settled: an atlas
// that holds far more room than the frames use reports full, so that
// MakeRoom lets go of what no frame draws any more and of the memory it
// took, with no frame more. A frame painted in part drew too little to
// tell.
func (s *System) Review(window uintptr, settled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if window != s.reviewer {
		s.reviewer, s.sharedAt = window, s.frame
	}
	if s.rooms > 0 || !settled && s.frame-s.reviewed < atlasReviewFrames {
		return
	}
	atlases := [2]*scene.Atlas{s.MaskAtlas, s.ColorAtlas}
	large := func(i int, used int) bool {
		a := atlases[i]
		return min(a.W, a.H) >= 2*atlasSmallest && used*atlasShrinkFill <= a.W*a.H && a.Generation() != s.unshrunk[i]
	}
	// Most frames end here: what they drew alone fills the atlas enough.
	if !large(0, s.drawn[0]) && !large(1, s.drawn[1]) {
		return
	}
	s.reviewed = s.frame
	since := s.liveSince()
	var used [2]int
	var side [2]int
	add := func(color bool, g GlyphImage) {
		i := 0
		if color {
			i = 1
		}
		used[i] += (int(g.W) + 1) * (int(g.H) + 1)
		side[i] = max(side[i], int(g.W)+1, int(g.H)+1)
	}
	for _, e := range s.masks {
		if e.used >= since {
			add(false, e.GlyphImage)
		}
	}
	for _, e := range s.glyphs {
		if e.OK && e.used >= since {
			add(e.inColor(), e.GlyphImage)
		}
	}
	for _, e := range s.runs {
		if e.used >= since {
			add(e.inColor(), e.GlyphImage)
		}
	}
	for _, g := range s.transient {
		add(false, g)
	}
	for i, a := range atlases {
		// A bitmap too large for half the atlas keeps it from halving.
		if large(i, used[i]) && side[i] <= min(a.W, a.H)/2 {
			s.full[i], s.reviewing = true, true
		}
	}
}

// liveSince returns the first frame whose drawing an atlas review keeps:
// the window's own, unless other windows took turns with it of late.
func (s *System) liveSince() uint64 {
	if s.frame-s.sharedAt >= atlasLiveFrames {
		return s.frame
	}
	if s.frame < atlasLiveFrames {
		return 0
	}
	return s.frame - atlasLiveFrames + 1
}

// MakeRoom makes room in the atlases that filled up: it keeps what the
// frame being painted drew, growing the atlas when that and what the frame
// still needs take much of it, or when room made for the frame before did
// not suffice, shrinking it when they take little of it, and forgets
// everything else. Everything kept moves, so the frame must be painted
// again.
func (s *System) MakeRoom() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms++
	since := s.frame
	if s.reviewing {
		since = s.liveSince()
	}
	for i, color := range [2]bool{false, true} {
		if s.full[i] {
			s.makeRoom(color, s.want[i], s.wantSize[i], s.rooms > 1, since)
		}
	}
	s.forgetLeftOut()
}

func (s *System) makeRoom(color bool, want int, size image.Point, grow bool, since uint64) {
	a := s.MaskAtlas
	if color {
		a = s.ColorAtlas
	}
	var keep []*atlasEntry
	need := want
	if !color {
		for _, g := range s.transient {
			need += (int(g.W) + 1) * (int(g.H) + 1)
		}
		clear(s.transient)
		a.ResetTransient()
		for k, e := range s.masks {
			if e.used < since {
				delete(s.masks, k)
				continue
			}
			keep = append(keep, e)
		}
	}
	for k, e := range s.glyphs {
		if !e.OK || e.inColor() != color {
			continue // blank glyphs take no room
		}
		if e.used < since {
			delete(s.glyphs, k)
			continue
		}
		keep = append(keep, e)
	}
	for k, e := range s.runs {
		if e.inColor() != color {
			continue
		}
		if e.used < since {
			delete(s.runs, k)
			continue
		}
		keep = append(keep, e)
	}
	rects := make([]image.Rectangle, len(keep))
	for i, e := range keep {
		rects[i] = image.Rect(int(e.X), int(e.Y), int(e.X)+int(e.W), int(e.Y)+int(e.H))
		need += (int(e.W) + 1) * (int(e.H) + 1)
	}
	// Shelves waste room: keep the atlas at most half full.
	if grow {
		a.Grow()
	}
	for (need*2 > a.W*a.H || size.X > a.W || size.Y > a.H) && a.Grow() {
	}
	w, h := a.W, a.H
	for !grow && min(w, h) >= 2*atlasSmallest && need*atlasShrinkFill <= w*h && max(size.X, size.Y) <= min(w, h)/2 {
		w, h = w/2, h/2
	}
	pos, ok := a.RepackTo(w, h, rects)
	if !ok {
		// The bitmaps do not pack into the smaller atlas, though they would
		// take little of it: reviews leave this one alone until it changes.
		if pos, ok = a.Repack(rects); ok {
			i := 0
			if color {
				i = 1
			}
			s.unshrunk[i] = a.Generation()
		}
	}
	if !ok {
		a.Reset()
		for k, e := range s.glyphs {
			if e.OK && e.inColor() == color {
				delete(s.glyphs, k)
			}
		}
		for k, e := range s.runs {
			if e.inColor() == color {
				delete(s.runs, k)
			}
		}
		if !color {
			clear(s.masks)
		}
		return
	}
	for i, e := range keep {
		e.X, e.Y = uint16(pos[i].X), uint16(pos[i].Y)
	}
}

// Glyph rasterizes glyph id of a font at scale pixels per DIP whose pen
// is x device pixels from the left, for text of a shade, placed as the
// system's text stack places glyphs: at one of the positions within a
// pixel it draws them at. Left is relative to the pixel x is in. Glyphs on
// opaque backgrounds may be subpixel ones, when the system's settings
// (TextParams) ask for subpixel antialiasing.
func (s *System) Glyph(f *Font, id uint32, scale, x float32, shade Shade, opaque bool) GlyphImage {
	return s.glyph(f, id, scale, x, shade, opaque, 0, 0, nil)
}

// StrokeGlyph returns the coverage mask of a stroke width device pixels
// wide along the outline of the glyph Glyph returns for the same
// arguments, centered on it and round at its corners, placed as that
// glyph is: a mask glyph, for color glyphs too, which strokes the edge of
// their pixels. Text stroked under its fill (CSS's paint-order: stroke
// fill) shows the half outside the glyph.
func (s *System) StrokeGlyph(f *Font, id uint32, scale, x float32, shade Shade, opaque bool, width float32) GlyphImage {
	w := min(max(width*16+0.5, 1), 128*16) // at most 128 pixels
	return s.glyph(f, id, scale, x, shade, opaque, uint16(w), 0, (*System).rasterizeStroke)
}

// ShadowGlyph returns the coverage mask of the glyph Glyph returns for the
// same arguments blurred by a Gaussian of standard deviation sigma device
// pixels, placed as that glyph is, wider by the blur: a mask glyph, for
// color glyphs too, of their pixels' alpha, which a text's shadow (CSS's
// text-shadow) paints in its color.
func (s *System) ShadowGlyph(f *Font, id uint32, scale, x float32, shade Shade, opaque bool, sigma float32) GlyphImage {
	b := min(max(sigma*16+0.5, 0), 64*16) // at most 64 pixels
	return s.glyph(f, id, scale, x, shade, opaque, 0, 1+uint16(b), (*System).rasterizeShadow)
}

// glyph returns the glyph of Glyph, or with stroke the mask of a stroke
// stroke/16 pixels wide along it, or with shadow the mask of it blurred
// by (shadow-1)/16 pixels, which derive rasterizes (StrokeGlyph,
// ShadowGlyph): apps that stroke and shadow no text do not link it.
func (s *System) glyph(f *Font, id uint32, scale, x float32, shade Shade, opaque bool, stroke, shadow uint16, derive func(s *System, f *Font, id uint32, scale, dx float32, shade Shade, amount float32) GlyphImage) GlyphImage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f == nil {
		return GlyphImage{}
	}
	shade = f.shadeFor(shade)
	subpixel := opaque && s.subpixel
	q, n, carry := s.place(f, scale, subpixel, x)
	key := glyphKey{font: f, id: id, scale: uint32(scale*256 + 0.5), subX: uint8(q), shade: shade, subpixel: subpixel, stroke: stroke, shadow: shadow}
	e, ok := s.glyphs[key]
	if !ok {
		if _, out := s.outGlyphs[key]; out {
			return GlyphImage{}
		}
		failed := s.failed
		var g GlyphImage
		switch {
		case stroke > 0:
			g = derive(s, f, id, scale, float32(q)/float32(n), key.shade, float32(stroke)/16)
		case shadow > 0:
			g = derive(s, f, id, scale, float32(q)/float32(n), key.shade, float32(shadow-1)/16)
		default:
			g = s.rasterize(f, id, scale, float32(q)/float32(n), key.shade, key.subpixel)
		}
		e = &atlasEntry{GlyphImage: g, used: s.frame - 1}
		if s.failed == failed { // not left out of a full atlas
			s.glyphs[key] = e
		} else {
			if s.outGlyphs == nil {
				s.outGlyphs = map[glyphKey]struct{}{}
			}
			s.outGlyphs[key] = struct{}{}
		}
	}
	s.touch(e)
	g := e.GlyphImage
	g.Left += carry
	return g
}

// Origin returns where Glyph puts the origin of a glyph of f whose pen is
// x device pixels from the left: at the position within a pixel the
// system's text stack draws it at.
func (s *System) Origin(f *Font, scale, x float32, opaque bool) float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f == nil {
		return x
	}
	q, n, carry := s.place(f, scale, opaque && s.subpixel, x)
	return float32(math.Floor(float64(x))) + carry + float32(q)/float32(n)
}

// place returns where the system's text stack draws a glyph of f whose pen
// is at x: at position q of the n within a pixel, carry pixels right of
// the pixel x is in.
func (s *System) place(f *Font, scale float32, subpixel bool, x float32) (q, n int, carry float32) {
	pk := placeKey{f, uint32(scale*256 + 0.5), subpixel}
	// The glyphs of a text come one after the other in its font.
	pl := s.lastPlace
	if pk != s.lastPlaceKey {
		var ok bool
		if pl, ok = s.places[pk]; !ok {
			pl.n, pl.round = s.engine().positions(f, scale, subpixel)
			pl.n = max(pl.n, 1)
			s.places[pk] = pl
		}
		s.lastPlaceKey, s.lastPlace = pk, pl
	}
	frac := float64(x) - math.Floor(float64(x))
	if pl.round {
		if q = int(math.Floor(frac*float64(pl.n) + 0.5)); q == pl.n {
			return 0, pl.n, 1
		}
		return q, pl.n, 0
	}
	return min(int(frac*float64(pl.n)), pl.n-1), pl.n, 0
}

// Baseline returns where the system's text stack draws a baseline that is
// y device pixels from the top: on a whole pixel.
func (s *System) Baseline(y float32) float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.engine().baseline(y)
}

func (s *System) rasterize(f *Font, id uint32, scale, dx float32, shade Shade, subpixel bool) GlyphImage {
	b := s.engine().glyph(f, id, scale, dx, shade, subpixel)
	if b.w <= 0 || b.h <= 0 || b.w > 2048 || b.h > 2048 {
		return GlyphImage{}
	}
	inColor := b.color || b.subpixel
	x, y, ok := s.alloc(inColor, b.w, b.h)
	if !ok {
		return GlyphImage{}
	}
	if inColor {
		s.ColorAtlas.Put(x, y, b.w, b.h, b.pix, 4*b.w)
	} else {
		s.MaskAtlas.Put(x, y, b.w, b.h, b.pix, b.w)
	}
	return GlyphImage{OK: true, Colored: b.color, Subpixel: b.subpixel, Thin: f.thin && !b.color,
		X: uint16(x), Y: uint16(y), W: uint16(b.w), H: uint16(b.h), Left: float32(b.left), Top: float32(b.top)}
}

// rasterizeStroke puts the mask of a stroke width pixels wide along the
// outline of a glyph into the mask atlas (StrokeGlyph).
func (s *System) rasterizeStroke(f *Font, id uint32, scale, dx float32, shade Shade, width float32) GlyphImage {
	b := s.engine().glyph(f, id, scale, dx, shade, false)
	if b.w <= 0 || b.h <= 0 || b.w > 2048 || b.h > 2048 {
		return GlyphImage{}
	}
	b = strokeBitmap(b, width/2)
	if b.w > 2048 || b.h > 2048 {
		return GlyphImage{}
	}
	x, y, ok := s.alloc(false, b.w, b.h)
	if !ok {
		return GlyphImage{}
	}
	s.MaskAtlas.Put(x, y, b.w, b.h, b.pix, b.w)
	return GlyphImage{OK: true, X: uint16(x), Y: uint16(y), W: uint16(b.w), H: uint16(b.h), Left: float32(b.left), Top: float32(b.top)}
}

// rasterizeShadow puts the mask of a glyph blurred by sigma pixels into
// the mask atlas (ShadowGlyph).
func (s *System) rasterizeShadow(f *Font, id uint32, scale, dx float32, shade Shade, sigma float32) GlyphImage {
	b := s.engine().glyph(f, id, scale, dx, shade, false)
	if b.w <= 0 || b.h <= 0 || b.w > 2048 || b.h > 2048 {
		return GlyphImage{}
	}
	b = blurBitmap(b, sigma)
	if b.w > 2048 || b.h > 2048 {
		return GlyphImage{}
	}
	x, y, ok := s.alloc(false, b.w, b.h)
	if !ok {
		return GlyphImage{}
	}
	s.MaskAtlas.Put(x, y, b.w, b.h, b.pix, b.w)
	return GlyphImage{OK: true, X: uint16(x), Y: uint16(y), W: uint16(b.w), H: uint16(b.h), Left: float32(b.left), Top: float32(b.top)}
}

// TextParams returns how renderers correct the coverage of glyphs, as
// the system's settings say, for the frame being painted, and has glyphs
// on opaque backgrounds take subpixel antialiasing if they ask for it.
func (s *System) TextParams() scene.TextParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, subpixel := s.engine().textParams()
	s.subpixel = subpixel
	return p
}

// alloc finds lasting room in an atlas, or records that it is full.
func (s *System) alloc(color bool, w, h int) (int, int, bool) {
	a := s.MaskAtlas
	if color {
		a = s.ColorAtlas
	}
	// The first bitmap can size an empty atlas without invalidating any
	// glyphs already painted. In particular, the color atlas starts with
	// one transparent texel and needs no repaint to show its first emoji.
	if a.Version() == 0 {
		for (w+1 > a.W || h+1 > a.H) && a.Grow() {
		}
	}
	if x, y, ok := a.Alloc(w, h); ok {
		return x, y, true
	}
	s.leftOut(color, w, h)
	return 0, 0, false
}

func (s *System) leftOut(color bool, w, h int) {
	i := 0
	if color {
		i = 1
	}
	s.full[i] = true
	s.want[i] += (w + 1) * (h + 1)
	s.wantSize[i].X = max(s.wantSize[i].X, w+1)
	s.wantSize[i].Y = max(s.wantSize[i].Y, h+1)
	s.failed++
}

// Mask returns a coverage mask cached under key in the mask atlas,
// drawing it when it is not: draw returns its size and one byte per pixel.
// Icons and other vector shapes use it.
//
// A mask a frame draws for the first time may be one frame of an
// animation, never drawn again: it lasts for that frame only, in the
// atlas's transient room, and is cached once a later frame draws it too.
func (s *System) Mask(key uint64, draw func() (w, h int, pix []byte)) GlyphImage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.masks[key]; ok {
		s.touch(e)
		return e.GlyphImage
	}
	if g, ok := s.transient[key]; ok {
		s.transientUses++
		return g
	}
	if _, out := s.outMasks[key]; out {
		return GlyphImage{}
	}
	last, seen := s.recent[key]
	lasting := seen && last != s.frame
	w, h, pix := draw()
	if w <= 0 || h <= 0 {
		return GlyphImage{}
	}
	var x, y int
	var ok bool
	if lasting {
		x, y, ok = s.MaskAtlas.Alloc(w, h)
	} else {
		x, y, ok = s.MaskAtlas.AllocTransient(w, h)
	}
	if !ok {
		s.leftOut(false, w, h)
		if s.outMasks == nil {
			s.outMasks = map[uint64]struct{}{}
		}
		s.outMasks[key] = struct{}{}
		return GlyphImage{}
	}
	s.MaskAtlas.Put(x, y, w, h, pix, w)
	g := GlyphImage{OK: true, X: uint16(x), Y: uint16(y), W: uint16(w), H: uint16(h)}
	if lasting {
		delete(s.recent, key)
		e := &atlasEntry{GlyphImage: g, used: s.frame - 1}
		s.touch(e)
		s.masks[key] = e
	} else {
		s.transient[key] = g
		s.draws(false, g.W, g.H)
		s.recent[key] = s.frame
		s.transientUses++
	}
	return g
}

// Unkeep lets the masks cached under keys go as the atlas makes room for
// the frame being painted (MakeRoom), unless it draws them again first:
// masks it drew that the frame painted again draws otherwise, which the
// room made would keep but no paint draws.
func (s *System) Unkeep(keys []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if e, ok := s.masks[k]; ok && e.used == s.frame {
			e.used = s.frame - 1
		}
	}
}
