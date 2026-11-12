//go:build darwin && amd64

#include "textflag.h"

// returnZero is a C function that returns 0 in rax.
TEXT ·returnZero(SB), NOSPLIT|NOFRAME, $0-0
	XORL	AX, AX
	RET

// returnOne is a C function that returns 1 in rax.
TEXT ·returnOne(SB), NOSPLIT|NOFRAME, $0-0
	MOVL	$1, AX
	RET

// func returnZeroAddress() uintptr
TEXT ·returnZeroAddress(SB), NOSPLIT, $0-8
	MOVQ	$·returnZero(SB), AX
	MOVQ	AX, ret+0(FP)
	RET

// func returnOneAddress() uintptr
TEXT ·returnOneAddress(SB), NOSPLIT, $0-8
	MOVQ	$·returnOne(SB), AX
	MOVQ	AX, ret+0(FP)
	RET
