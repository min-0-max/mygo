package audio

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// macOS plays through an Audio Queue on the default output, which follows
// it when it changes.

type audioStreamBasicDescription struct {
	sampleRate       float64
	formatID         uint32
	formatFlags      uint32
	bytesPerPacket   uint32
	framesPerPacket  uint32
	bytesPerFrame    uint32
	channelsPerFrame uint32
	bitsPerChannel   uint32
	reserved         uint32
}

type audioQueueBuffer struct {
	audioDataBytesCapacity uint32
	audioData              unsafe.Pointer
	audioDataByteSize      uint32
	userData               uintptr
	packetDescCapacity     uint32
	packetDescs            uintptr
	packetDescCount        uint32
}

const (
	kAudioFormatLinearPCM   = 0x6C70636D // 'lpcm'
	kAudioFormatFlagIsFloat = 1
	aqFrames                = 512 // per buffer: 10.7 ms, as many as hold ahead
)

var (
	aqLoad sync.Once
	aqErr  error

	// The functions of Audio Queue Services, which run on the main thread
	// (mainthread.Forward).
	audioQueueNewOutput      uintptr
	audioQueueAllocateBuffer uintptr
	audioQueueEnqueueBuffer  uintptr
	audioQueueStart          uintptr
	audioQueueStop           uintptr
	audioQueueDispose        uintptr

	// aqCallback is created once; queues route buffers back to the device
	// open, through aqCurrent. It is the trampoline of the worker for the
	// purego callback, which the queue's own thread calls.
	aqCallback uintptr
	aqCurrent  atomic.Pointer[aqDevice]
)

func loadAudioToolbox() error {
	aqLoad.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			aqErr = err
			return
		}
		for _, f := range []struct {
			addr *uintptr
			name string
		}{
			{&audioQueueNewOutput, "AudioQueueNewOutput"},
			{&audioQueueAllocateBuffer, "AudioQueueAllocateBuffer"},
			{&audioQueueEnqueueBuffer, "AudioQueueEnqueueBuffer"},
			{&audioQueueStart, "AudioQueueStart"},
			{&audioQueueStop, "AudioQueueStop"},
			{&audioQueueDispose, "AudioQueueDispose"},
		} {
			if *f.addr, err = purego.Dlsym(lib, f.name); err != nil {
				aqErr = err
				return
			}
		}
		// The queue's own thread calls it when it is done with a buffer.
		aqCallback = mainthread.Callback(purego.NewCallback(func(user, q uintptr, b *audioQueueBuffer) {
			if d := aqCurrent.Load(); d != nil && d.q == q {
				select {
				case d.free <- b:
				default:
				}
			}
		}))
	})
	return aqErr
}

type aqDevice struct {
	q       uintptr
	free    chan *audioQueueBuffer
	buffers int
	queued  int // buffers enqueued before the queue started
	started bool
}

func openDevice(ahead time.Duration) (device, int, error) {
	if err := loadAudioToolbox(); err != nil {
		return nil, 0, err
	}
	desc := audioStreamBasicDescription{
		sampleRate:       sampleRate,
		formatID:         kAudioFormatLinearPCM,
		formatFlags:      kAudioFormatFlagIsFloat,
		bytesPerPacket:   channels * 4,
		framesPerPacket:  1,
		bytesPerFrame:    channels * 4,
		channelsPerFrame: channels,
		bitsPerChannel:   32,
	}
	n := max(2, int((int64(ahead)*sampleRate/int64(time.Second)+aqFrames-1)/aqFrames))
	d := &aqDevice{free: make(chan *audioQueueBuffer, n), buffers: n}
	if st := mainthread.Forward[int32](audioQueueNewOutput, &desc, aqCallback, uintptr(0), uintptr(0), uintptr(0), uint32(0), &d.q); st != 0 {
		return nil, 0, fmt.Errorf("audio: AudioQueueNewOutput failed (OSStatus %d)", st)
	}
	for range n {
		var b *audioQueueBuffer
		if st := mainthread.Forward[int32](audioQueueAllocateBuffer, d.q, uint32(aqFrames*channels*4), &b); st != 0 {
			mainthread.Forward[int32](audioQueueDispose, d.q, true)
			return nil, 0, fmt.Errorf("audio: AudioQueueAllocateBuffer failed (OSStatus %d)", st)
		}
		d.free <- b
	}
	aqCurrent.Store(d)
	return d, aqFrames, nil
}

func (d *aqDevice) write(samples []float32) error {
	var b *audioQueueBuffer
	select {
	case b = <-d.free:
	case <-time.After(time.Second):
		// The queue stopped taking buffers, as when its device went.
		return fmt.Errorf("audio: the audio queue stopped")
	}
	copy(unsafe.Slice((*float32)(b.audioData), len(samples)), samples)
	b.audioDataByteSize = uint32(len(samples) * 4)
	if st := mainthread.Forward[int32](audioQueueEnqueueBuffer, d.q, b, uint32(0), uintptr(0)); st != 0 {
		return fmt.Errorf("audio: AudioQueueEnqueueBuffer failed (OSStatus %d)", st)
	}
	if !d.started {
		// Start once every buffer holds something, not to begin with an
		// underrun.
		if d.queued++; d.queued == d.buffers {
			if st := mainthread.Forward[int32](audioQueueStart, d.q, uintptr(0)); st != 0 {
				return fmt.Errorf("audio: AudioQueueStart failed (OSStatus %d)", st)
			}
			d.started = true
		}
	}
	return nil
}

func (d *aqDevice) close() {
	aqCurrent.CompareAndSwap(d, nil)
	mainthread.Forward[int32](audioQueueStop, d.q, true)
	mainthread.Forward[int32](audioQueueDispose, d.q, true)
}
