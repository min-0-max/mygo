package gpu

import "image"

// FrameOutcome is how a frame presented.
type FrameOutcome uint8

const (
	// FrameTransaction presented with the Core Animation transaction.
	FrameTransaction FrameOutcome = iota
	// FrameImmediate presented as soon as the GPU had it.
	FrameImmediate
	// FrameStashed waits for a drawable in flight, and commits outside its
	// frame.
	FrameStashed
	// FrameSkipped found no drawable, and its command buffer holds only what
	// it drew before one.
	FrameSkipped
	// FrameUndamaged changed nothing, and drew nothing.
	FrameUndamaged
	// FrameHeld is left to the frame after it.
	FrameHeld
)

// FrameFacts is what came of the last frame a renderer was given.
type FrameFacts struct {
	Outcome FrameOutcome
	// Request is when the frame asked for its presentation, 0 when it did
	// not, and DrawableWait how long it waited for a drawable, on the clock
	// the renderer reports with.
	Request, DrawableWait uint64
	// Damage is the pixels the frame changed.
	Damage int
	// CommandBuffer tells that the frame committed a command buffer.
	CommandBuffer bool
}

// CommitKind is what a command buffer committed outside a frame drew.
type CommitKind uint8

const (
	// CommitStashed is a stashed frame, drawn into its drawable or dropped.
	CommitStashed CommitKind = iota
	// CommitOffscreen is an offscreen canvas.
	CommitOffscreen
	// CommitPixels is a render to pixels.
	CommitPixels
)

// DamageArea returns the pixels that damage covers, the whole frame of w×h
// when it is not known.
func DamageArea(damage []image.Rectangle, known bool, w, h int) int {
	if !known {
		return w * h
	}
	area := 0
	for _, r := range damage {
		area += r.Dx() * r.Dy()
	}
	return area
}
