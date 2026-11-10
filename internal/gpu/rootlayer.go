package gpu

// RootLayerKind names an observed root drawable property change or request.
// None of these events acknowledges a compositor presentation.
type RootLayerKind uint8

const (
	RootLayerAttached RootLayerKind = iota
	RootLayerResized
	RootLayerTrimmed
	RootLayerPresentRequested
)

// RootLayerRecord is an immutable observation on the explicitly supplied
// recorder clock. At and LastRequestAt share that clock, not SCK displayTime.
// The caller must declare/map its clock before joining another clock domain.
// Layer identifies an observation attachment; Sequence starts at one, is
// contiguous, and must not wrap. A consumer must reject lost sequences.
type RootLayerRecord struct {
	Kind                RootLayerKind
	Layer, Sequence, At uint64
	// SizeKnown is false before this renderer first sets drawableSize.
	SizeKnown                          bool
	Width, Height                      int
	LastRequestSequence, LastRequestAt uint64
}
