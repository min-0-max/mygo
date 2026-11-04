//go:build darwin && arm64

#include "textflag.h"
#include "go_asm.h"

// The frame of callRecord: the stack arguments of the call it makes at its
// bottom, then the registers it saves.
#define ARGS (const_stackWords*8)
#define FRAME (ARGS+32)

// callRecord(rec *record) is a C function that calls rec.fn with the
// argument registers and the stack words of rec and stores the result
// registers in rec; with recordRetainsResult, it retains the object that
// the call returns.
TEXT ·callRecord(SB), NOSPLIT|NOFRAME, $0-0
	SUB	$FRAME, RSP
	STP	(R29, R30), ARGS(RSP)
	STP	(R19, R27), (ARGS+16)(RSP)
	ADD	$ARGS, RSP, R29
	MOVD	R0, R19

	LDP	(record_stack+0)(R19), (R2, R3)
	STP	(R2, R3), 0(RSP)
	LDP	(record_stack+16)(R19), (R2, R3)
	STP	(R2, R3), 16(RSP)
	LDP	(record_stack+32)(R19), (R2, R3)
	STP	(R2, R3), 32(RSP)
	LDP	(record_stack+48)(R19), (R2, R3)
	STP	(R2, R3), 48(RSP)
	LDP	(record_stack+64)(R19), (R2, R3)
	STP	(R2, R3), 64(RSP)
	LDP	(record_stack+80)(R19), (R2, R3)
	STP	(R2, R3), 80(RSP)
	LDP	(record_stack+96)(R19), (R2, R3)
	STP	(R2, R3), 96(RSP)
	LDP	(record_stack+112)(R19), (R2, R3)
	STP	(R2, R3), 112(RSP)

	FMOVD	(record_d+0)(R19), F0
	FMOVD	(record_d+8)(R19), F1
	FMOVD	(record_d+16)(R19), F2
	FMOVD	(record_d+24)(R19), F3
	FMOVD	(record_d+32)(R19), F4
	FMOVD	(record_d+40)(R19), F5
	FMOVD	(record_d+48)(R19), F6
	FMOVD	(record_d+56)(R19), F7
	MOVD	record_x8(R19), R8
	MOVD	record_fn(R19), R16
	LDP	(record_x+0)(R19), (R0, R1)
	LDP	(record_x+16)(R19), (R2, R3)
	LDP	(record_x+32)(R19), (R4, R5)
	LDP	(record_x+48)(R19), (R6, R7)
	CALL	R16

	STP	(R0, R1), record_r(R19)
	FMOVD	F0, (record_rd+0)(R19)
	FMOVD	F1, (record_rd+8)(R19)
	FMOVD	F2, (record_rd+16)(R19)
	FMOVD	F3, (record_rd+24)(R19)

	// The caller keeps the object that the call returns: take its
	// reference on the thread that made the call.
	MOVD	record_flags(R19), R1
	TBZ	$const_recordRetainsResultBit, R1, return
	CBZ	R0, return
	MOVD	$·link(SB), R1
	MOVD	linkState_retain(R1), R1
	CALL	R1
return:

	LDP	(ARGS+16)(RSP), (R19, R27)
	LDP	ARGS(RSP), (R29, R30)
	ADD	$FRAME, RSP
	RET

// func callRecordAddress() uintptr
TEXT ·callRecordAddress(SB), NOSPLIT, $0-8
	MOVD	$·callRecord(SB), R0
	MOVD	R0, ret+0(FP)
	RET
