//go:build darwin && amd64 && mainthreadcheck

#include "textflag.h"

// C ABI: checkedCallRecord(rec in rdi, expectedThread in rsi) -> rax.
// Read the same native identity as threadPointer, without entering Go.
TEXT ·checkedCallRecord(SB), NOSPLIT|NOFRAME, $0-0
	BYTE $0x65; BYTE $0x48; BYTE $0x8b; BYTE $0x04; BYTE $0x25; LONG $0 // MOVQ %gs:0, AX
	CMPQ AX, SI
	JNE mismatch
	PUSHQ BP
	MOVQ SP, BP
	CALL ·callRecord(SB)
	POPQ BP
	XORQ AX, AX
mismatch:
	RET

TEXT ·checkedCallRecordAddress(SB), NOSPLIT, $0-8
	MOVQ $·checkedCallRecord(SB), AX
	MOVQ AX, ret+0(FP)
	RET
