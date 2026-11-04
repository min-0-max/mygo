package text

import (
	"testing"

	"github.com/egoist/mygo/internal/scene"
)

// A window's review counts what other windows drew of late, which takes
// turns with it: the mask of a window that does not draw in that frame
// stays, and once the other windows stop, what they drew goes.
func TestAReviewKeepsWhatOtherWindowsDrew(t *testing.T) {
	s := newSystem()
	s.MaskAtlas = scene.NewAtlas(1, 4096, 4096)
	draws := 0
	mask := func() (int, int, []byte) {
		draws++
		return 300, 300, make([]byte, 300*300)
	}
	frame := func(window uintptr, draw bool) {
		t.Helper()
		s.BeginFrame()
		if draw {
			if g := s.Mask(7, mask); !g.OK {
				t.Fatal("the mask did not draw")
			}
		}
		s.Review(window, true)
		if s.Full() {
			s.MakeRoom()
		}
		s.EndFrame()
	}
	frame(1, true)
	frame(1, true)
	for range atlasLiveFrames {
		frame(1, true)
	}
	if draws != 2 {
		t.Fatalf("the mask drew %d times", draws)
	}
	frame(2, false)
	if _, ok := s.masks[7]; !ok {
		t.Fatal("the review of a window that drew nothing let go of another's mask")
	}
	if s.MaskAtlas.W > 2048 {
		t.Errorf("little of the atlas in use left it %d pixels a side", s.MaskAtlas.W)
	}
	for range atlasLiveFrames {
		frame(2, false)
	}
	if _, ok := s.masks[7]; ok {
		t.Error("the mask stayed after every window stopped drawing it")
	}
	if s.MaskAtlas.W != atlasSmallest {
		t.Errorf("the empty atlas holds %d pixels a side", s.MaskAtlas.W)
	}
}

// A bitmap too large for half an atlas keeps it from halving, and frames
// neither try nor pay for it.
func TestAReviewLeavesAnAtlasItCannotHalve(t *testing.T) {
	s := newSystem()
	s.MaskAtlas = scene.NewAtlas(1, 1024, 1024)
	mask := func() (int, int, []byte) { return 700, 3, make([]byte, 2100) }
	frame := func() {
		s.BeginFrame()
		s.Mask(9, mask)
		s.Review(1, true)
		if s.Full() {
			s.MakeRoom()
		}
		s.EndFrame()
	}
	for range atlasLiveFrames + 2 {
		frame()
	}
	gen := s.MaskAtlas.Generation()
	for range 10 {
		frame()
	}
	if s.MaskAtlas.W != 1024 || s.MaskAtlas.Generation() != gen {
		t.Errorf("the atlas went to %d pixels a side, generation %d to %d", s.MaskAtlas.W, gen, s.MaskAtlas.Generation())
	}
}
