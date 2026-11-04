//go:build darwin && amd64

#include "textflag.h"
#include "go_asm.h"

// callRecord(rec *record) is a C function that calls rec.fn with the
// argument registers and the stack words of rec and stores the result
// registers in rec; with recordRetainsResult, it retains the object that
// the call returns.
TEXT ·callRecord(SB), NOSPLIT|NOFRAME, $0-0
	PUSHQ	BP
	MOVQ	SP, BP
	PUSHQ	BX
	PUSHQ	R12
	SUBQ	$(const_stackWords*8), SP
	MOVQ	DI, BX
	MOVQ	(record_stack+0)(BX), AX
	MOVQ	AX, 0(SP)
	MOVQ	(record_stack+8)(BX), AX
	MOVQ	AX, 8(SP)
	MOVQ	(record_stack+16)(BX), AX
	MOVQ	AX, 16(SP)
	MOVQ	(record_stack+24)(BX), AX
	MOVQ	AX, 24(SP)
	MOVQ	(record_stack+32)(BX), AX
	MOVQ	AX, 32(SP)
	MOVQ	(record_stack+40)(BX), AX
	MOVQ	AX, 40(SP)
	MOVQ	(record_stack+48)(BX), AX
	MOVQ	AX, 48(SP)
	MOVQ	(record_stack+56)(BX), AX
	MOVQ	AX, 56(SP)
	MOVQ	(record_stack+64)(BX), AX
	MOVQ	AX, 64(SP)
	MOVQ	(record_stack+72)(BX), AX
	MOVQ	AX, 72(SP)
	MOVQ	(record_stack+80)(BX), AX
	MOVQ	AX, 80(SP)
	MOVQ	(record_stack+88)(BX), AX
	MOVQ	AX, 88(SP)
	MOVQ	(record_stack+96)(BX), AX
	MOVQ	AX, 96(SP)
	MOVQ	(record_stack+104)(BX), AX
	MOVQ	AX, 104(SP)
	MOVQ	(record_stack+112)(BX), AX
	MOVQ	AX, 112(SP)
	MOVQ	(record_stack+120)(BX), AX
	MOVQ	AX, 120(SP)
	MOVSD	(record_xmm+0)(BX), X0
	MOVSD	(record_xmm+8)(BX), X1
	MOVSD	(record_xmm+16)(BX), X2
	MOVSD	(record_xmm+24)(BX), X3
	MOVSD	(record_xmm+32)(BX), X4
	MOVSD	(record_xmm+40)(BX), X5
	MOVSD	(record_xmm+48)(BX), X6
	MOVSD	(record_xmm+56)(BX), X7
	MOVQ	record_fn(BX), R11
	MOVQ	(record_x+8)(BX), SI
	MOVQ	(record_x+16)(BX), DX
	MOVQ	(record_x+24)(BX), CX
	MOVQ	(record_x+32)(BX), R8
	MOVQ	(record_x+40)(BX), R9
	MOVQ	(record_x+0)(BX), DI
	MOVQ	record_al(BX), AX
	CALL	R11
	MOVQ	AX, (record_r+0)(BX)
	MOVQ	DX, (record_r+8)(BX)
	MOVSD	X0, (record_rx+0)(BX)
	MOVSD	X1, (record_rx+8)(BX)

	// The caller keeps the object that the call returns: take its
	// reference on the thread that made the call.
	MOVQ	record_flags(BX), CX
	BTQ	$const_recordRetainsResultBit, CX
	JCC	return
	TESTQ	AX, AX
	JZ	return
	MOVQ	AX, DI
	MOVQ	$·link(SB), AX
	MOVQ	linkState_retain(AX), AX
	CALL	AX
return:
	ADDQ	$(const_stackWords*8), SP
	POPQ	R12
	POPQ	BX
	POPQ	BP
	RET

// func callRecordAddress() uintptr
TEXT ·callRecordAddress(SB), NOSPLIT, $0-8
	MOVQ	$·callRecord(SB), AX
	MOVQ	AX, ret+0(FP)
	RET
