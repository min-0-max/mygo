//go:build darwin && arm64

#include "textflag.h"

// returnZero is a C function that returns 0 in x0.
TEXT ·returnZero(SB), NOSPLIT|NOFRAME, $0-0
	MOVD	$0, R0
	RET

// returnOne is a C function that returns 1 in x0.
TEXT ·returnOne(SB), NOSPLIT|NOFRAME, $0-0
	MOVD	$1, R0
	RET

// func returnZeroAddress() uintptr
TEXT ·returnZeroAddress(SB), NOSPLIT, $0-8
	MOVD	$·returnZero(SB), R0
	MOVD	R0, ret+0(FP)
	RET

// func returnOneAddress() uintptr
TEXT ·returnOneAddress(SB), NOSPLIT, $0-8
	MOVD	$·returnOne(SB), R0
	MOVD	R0, ret+0(FP)
	RET
