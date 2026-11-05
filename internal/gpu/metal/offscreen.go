//go:build darwin

package metal

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/gpu"
	"github.com/egoist/mygo/internal/scene"
)

// RenderOffscreen draws s with a renderer of its own into a texture,
// without a layer, and returns its premultiplied BGRA rows: for tests, as
// packages drawing effects have.
func RenderOffscreen(s *scene.Scene) ([]byte, error) {
	r, err := newRenderer()
	if err != nil {
		return nil, err
	}
	defer r.Release()
	return r.renderOffscreen(s)
}

// Offscreen draws frames with a renderer of its own into a texture,
// without a layer, waiting for each and timing it on the GPU: for
// benchmarks, as those of package ui drawing its frames.
type Offscreen struct {
	r    *Renderer
	tex  id
	w, h int
	// encoded is how long encoding the last frame took on the CPU.
	encoded time.Duration
}

var msgDouble func(obj id, sel uintptr) float64

// NewOffscreen returns an Offscreen drawing frames of w×h pixels.
func NewOffscreen(w, h int) (*Offscreen, error) {
	r, err := newRenderer()
	if err != nil {
		return nil, err
	}
	if msgDouble == nil {
		mainthread.BindAddr(&msgDouble, msgSend, "objc_msgSend")
	}
	o := &Offscreen{r: r}
	pool(func() { err = o.target(w, h) })
	if err != nil {
		o.Release()
		return nil, err
	}
	return o, nil
}

// target makes the texture drawn into w×h pixels.
func (o *Offscreen) target(w, h int) error {
	if o.tex != 0 && o.w == w && o.h == h {
		return nil
	}
	release(&o.tex)
	o.r.cur = &o.r.formats[0]
	o.tex = o.r.newTexture(w, h, pixelFormatBGRA8Unorm, usageRenderTarget|usageShaderRead, nil, 0)
	if o.tex == 0 {
		return errors.New("metal: cannot create a target")
	}
	o.w, o.h = w, h
	return nil
}

// Render draws s, a texture of its size taking the place of one of
// another, waits for the GPU to finish it, and returns the time the GPU
// spent on it, by its command buffer's timestamps.
func (o *Offscreen) Render(s *scene.Scene) (gpu time.Duration, err error) {
	pool(func() {
		r := o.r
		r.waitLast()
		if err = o.target(s.Width, s.Height); err != nil {
			return
		}
		var cb id
		start := time.Now()
		if cb, err = r.encode(s, o.tex); err != nil {
			return
		}
		o.encoded = time.Since(start)
		byReceiver(cb, "commit")
		r.committedOffscreen()
		send(cb, "waitUntilCompleted")
		if int(send(cb, "status")) == statusError {
			err = fmt.Errorf("metal: the frame failed: %s", describe(send(cb, "error")))
			return
		}
		secs := msgDouble(cb, uintptr(sel("GPUEndTime"))) - msgDouble(cb, uintptr(sel("GPUStartTime")))
		gpu = time.Duration(secs * float64(time.Second))
	})
	return gpu, err
}

// Encoded returns how long the CPU took to encode the last frame Render
// drew: building its instances (gpu.Builder), uploading what changed in
// its atlases, and encoding its passes, before committing them.
func (o *Offscreen) Encoded() time.Duration { return o.encoded }

// Pixels returns the premultiplied BGRA rows of the frame Render drew
// last.
func (o *Offscreen) Pixels() (pix []byte, err error) {
	pool(func() {
		if o.tex == 0 {
			err = errors.New("metal: no frame drawn")
			return
		}
		cb := send(o.r.queue, "commandBuffer")
		blit := send(cb, "blitCommandEncoder")
		send(blit, "synchronizeResource:", o.tex)
		send(blit, "endEncoding")
		byReceiver(cb, "commit")
		o.r.committedOutside(gpu.CommitPixels)
		send(cb, "waitUntilCompleted")
		if int(send(cb, "status")) == statusError {
			err = fmt.Errorf("metal: reading the frame failed: %s", describe(send(cb, "error")))
			return
		}
		pix = make([]byte, o.w*o.h*4)
		msgGetBytes(o.tex, sel("getBytes:bytesPerRow:fromRegion:mipmapLevel:"), unsafe.Pointer(&pix[0]), uint(o.w*4),
			mtlRegion{W: uint(o.w), H: uint(o.h), D: 1}, 0)
	})
	return pix, err
}

// HeldTextures returns the textures the renderer holds besides the one
// drawn into, with the bytes the GPU allocated for each.
func (o *Offscreen) HeldTextures() (held []HeldTexture) {
	pool(func() { held = o.r.heldTextures() })
	return held
}

// HeldBuffers returns the buffers of instances the renderer holds, with
// the bytes the GPU allocated for each; W is a buffer's length.
func (o *Offscreen) HeldBuffers() (held []HeldTexture) {
	pool(func() { held = o.r.heldBuffers() })
	return held
}

// HeldTextureBytes returns the bytes of the textures HeldTextures returns.
func (o *Offscreen) HeldTextureBytes() int {
	n := 0
	for _, t := range o.HeldTextures() {
		n += t.Bytes
	}
	return n
}

// DeviceAllocatedBytes returns the bytes the GPU device allocated for
// the process, every renderer's textures and buffers together
// (currentAllocatedSize). The reader of the call sites does not know the
// device that MTLCreateSystemDefaultDevice returns, and MTLHeap declares
// the message too, so it goes to the main thread.
func (o *Offscreen) DeviceAllocatedBytes() int {
	return int(mainthread.Forward[uint64](msgSend, o.r.device, sel("currentAllocatedSize")))
}

// TargetBytes returns the bytes the GPU allocated for the texture drawn
// into.
func (o *Offscreen) TargetBytes() (n int) {
	pool(func() {
		if o.tex != 0 {
			n = int(send(o.tex, "allocatedSize"))
		}
	})
	return n
}

// Release frees the texture and the renderer.
func (o *Offscreen) Release() {
	pool(func() { release(&o.tex) })
	o.r.Release()
}
