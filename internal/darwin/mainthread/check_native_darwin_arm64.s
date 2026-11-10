//go:build darwin && arm64 && mainthreadcheck

#include "textflag.h"

// C ABI: checkedCallRecord(rec in x0, expectedThread in x1) -> x0.
// Read the same native identity as threadPointer, without entering Go.
TEXT ·checkedCallRecord(SB), NOSPLIT|NOFRAME, $0-0
	WORD $0xd53bd062 // MRS TPIDRRO_EL0, x2
	AND $~7, R2
	CMP R1, R2
	BNE mismatch
	SUB $16, RSP
	STP (R29, R30), 0(RSP)
	MOVD RSP, R29
	CALL ·callRecord(SB)
	LDP 0(RSP), (R29, R30)
	ADD $16, RSP
	MOVD $0, R0
	RET
mismatch:
	MOVD R2, R0
	RET

TEXT ·checkedCallRecordAddress(SB), NOSPLIT, $0-8
	MOVD $·checkedCallRecord(SB), R0
	MOVD R0, ret+0(FP)
	RET
