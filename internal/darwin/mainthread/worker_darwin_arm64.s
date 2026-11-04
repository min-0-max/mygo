//go:build darwin && arm64

#include "textflag.h"
#include "go_asm.h"

// MRS TPIDRRO_EL0 into R0 and into R17: the thread pointer, whose low
// three bits the kernel may use.
#define MRS_TP_R0 WORD $0xd53bd060
#define MRS_TP_R17 WORD $0xd53bd071

// func threadPointer() uintptr
TEXT ·threadPointer(SB), NOSPLIT, $0-8
	MRS_TP_R0
	AND	$~7, R0
	MOVD	R0, ret+0(FP)
	RET

// exchange(rec *record) is a C function. Called with a record, it hands the
// record to the other side, the worker from the main thread and the main
// thread from the worker, and returns once the other side handed it back
// done. Called with 0 on the worker, it hands nothing and returns after
// the step that sets link.stop. Meanwhile it runs each record that the
// other side hands it: on the worker as a step, in an autorelease pool of
// its own, and on the main thread as a forwarded call; the main thread
// also runs the records that other threads queue.
TEXT ·exchange(SB), NOSPLIT|NOFRAME, $0-0
	SUB	$96, RSP
	STP	(R29, R30), 0(RSP)
	STP	(R19, R20), 16(RSP)
	STP	(R21, R22), 32(RSP)
	STP	(R23, R24), 48(RSP)
	STP	(R25, R26), 64(RSP)
	MOVD	R27, 80(RSP)
	MOVD	RSP, R29
	MOVD	R0, R19
	MOVD	$·link(SB), R21

	MRS_TP_R0
	AND	$~7, R0
	MOVD	linkState_mainThread(R21), R1
	CMP	R1, R0
	BNE	worker
	MOVD	$1, R20
	ADD	$linkState_toMain, R21, R22
	ADD	$linkState_toWorker, R21, R23
	MOVWU	linkState_mainSem(R21), R24
	MOVWU	linkState_workerSem(R21), R25
	B	start
worker:
	MOVD	$0, R20
	ADD	$linkState_toWorker, R21, R22
	ADD	$linkState_toMain, R21, R23
	MOVWU	linkState_workerSem(R21), R24
	MOVWU	linkState_mainSem(R21), R25

start:
	CBZ	R19, loop
	MOVD	R19, R1
	SWPALD	R1, (R23), R0
	CBNZ	R0, full
	MOVW	R25, R0
	MOVD	linkState_semaphoreSignal(R21), R1
	CALL	R1

loop:
	CBZ	R20, take
	MOVD	$0, R0
	CALL	·drainQueue(SB)
take:
	SWPALD	ZR, (R22), R26
	CBNZ	R26, got
	MOVW	R24, R0
	MOVD	linkState_semaphoreWait(R21), R1
	CALL	R1
	B	loop

got:
	TBZ	$0, R26, runit
	AND	$~1, R26
	CMP	R19, R26
	BEQ	done
	// The other side handed back a record that this side did not hand it.
	MOVD	$1, R0
	MOVD	R20, R1
	MOVD	R19, R2
	MOVD	R26, R3
	MOVD	linkState_violation(R21), R4
	CALL	R4
	UNDEF

runit:
	CBNZ	R20, onmain
	// A step on the worker.
	MOVD	linkState_depth(R21), R0
	ADD	$1, R0
	MOVD	R0, linkState_depth(R21)
	MOVD	linkState_poolPush(R21), R1
	CALL	R1
	MOVD	R0, 88(RSP)
	MOVD	R26, R0
	CALL	·callRecord(SB)
	// The pool of the worker does not take the object that the step
	// returns: the main thread autoreleases it in its own pool.
	MOVD	record_flags(R26), R1
	TBZ	$1, R1, pop
	MOVD	record_r(R26), R0
	CBZ	R0, pop
	MOVD	linkState_retain(R21), R1
	CALL	R1
pop:
	MOVD	88(RSP), R0
	MOVD	linkState_poolPop(R21), R1
	CALL	R1
	MOVD	linkState_depth(R21), R0
	SUB	$1, R0
	MOVD	R0, linkState_depth(R21)
	B	post
onmain:
	MOVD	R26, R0
	CALL	·callRecord(SB)

post:
	ORR	$1, R26, R1
	SWPALD	R1, (R23), R0
	CBNZ	R0, full
	MOVW	R25, R0
	MOVD	linkState_semaphoreSignal(R21), R1
	CALL	R1
	CBNZ	R19, loop
	MOVD	linkState_stop(R21), R0
	CBZ	R0, loop

done:
	MOVD	80(RSP), R27
	LDP	64(RSP), (R25, R26)
	LDP	48(RSP), (R23, R24)
	LDP	32(RSP), (R21, R22)
	LDP	16(RSP), (R19, R20)
	LDP	0(RSP), (R29, R30)
	ADD	$96, RSP
	RET

full:
	// The slot of the other side held R0 when this side handed it R1.
	MOVD	R0, R3
	MOVD	R1, R2
	MOVD	$2, R0
	MOVD	R20, R1
	MOVD	linkState_violation(R21), R4
	CALL	R4
	UNDEF

// drainQueue(info uintptr) is a C function, the perform function of the
// run loop source of the main thread: it runs each record that threads
// other than the worker queued and signals the semaphore of each.
TEXT ·drainQueue(SB), NOSPLIT|NOFRAME, $0-0
	SUB	$48, RSP
	STP	(R29, R30), 0(RSP)
	STP	(R19, R20), 16(RSP)
	STP	(R21, R27), 32(RSP)
	MOVD	RSP, R29
	MOVD	$·link(SB), R21
	ADD	$linkState_queue, R21, R0
	SWPALD	ZR, (R0), R19
next:
	CBZ	R19, out
	MOVD	record_next(R19), R20
	MOVD	R19, R0
	CALL	·callRecord(SB)
	MOVWU	record_sem(R19), R0
	MOVD	linkState_semaphoreSignal(R21), R1
	CALL	R1
	MOVD	R20, R19
	B	next
out:
	LDP	32(RSP), (R21, R27)
	LDP	16(RSP), (R19, R20)
	LDP	0(RSP), (R29, R30)
	ADD	$48, RSP
	RET

// The frame of a trampoline on the main thread: its record, then the
// registers it saves.
#define TFRAME (const_trampolineRecord+32)

// trampolineCommon is where entry R16 of trampolineTable branches. Off the
// main thread, or while the worker does not run, it branches to the C
// function of the entry with each register as it came. On the main thread
// it stores the argument registers and the stack arguments in a record on
// its stack, hands the record to the worker as a step and returns the
// result that the worker stores in it.
TEXT ·trampolineCommon(SB), NOSPLIT|NOFRAME, $0-0
	MRS_TP_R17
	AND	$~7, R17
	MOVD	$·link(SB), R9
	MOVD	linkState_mainThread(R9), R10
	CMP	R10, R17
	BNE	direct
	MOVD	linkState_running(R9), R10
	CBNZ	R10, step
direct:
	MOVD	$·targets(SB), R17
	MOVD	(R17)(R16<<3), R17
	JMP	(R17)

step:
	SUB	$TFRAME, RSP
	STP	(R29, R30), const_trampolineRecord(RSP)
	STP	(R19, R20), (const_trampolineRecord+16)(RSP)
	ADD	$const_trampolineRecord, RSP, R29
	MOVD	RSP, R19
	STP	(R0, R1), (record_x+0)(R19)
	STP	(R2, R3), (record_x+16)(R19)
	STP	(R4, R5), (record_x+32)(R19)
	STP	(R6, R7), (record_x+48)(R19)
	MOVD	R8, record_x8(R19)
	FMOVD	F0, (record_d+0)(R19)
	FMOVD	F1, (record_d+8)(R19)
	FMOVD	F2, (record_d+16)(R19)
	FMOVD	F3, (record_d+24)(R19)
	FMOVD	F4, (record_d+32)(R19)
	FMOVD	F5, (record_d+40)(R19)
	FMOVD	F6, (record_d+48)(R19)
	FMOVD	F7, (record_d+56)(R19)
	ADD	$TFRAME, RSP, R9
	LDP	0(R9), (R10, R11)
	STP	(R10, R11), (record_stack+0)(R19)
	LDP	16(R9), (R10, R11)
	STP	(R10, R11), (record_stack+16)(R19)
	LDP	32(R9), (R10, R11)
	STP	(R10, R11), (record_stack+32)(R19)
	LDP	48(R9), (R10, R11)
	STP	(R10, R11), (record_stack+48)(R19)
	LDP	64(R9), (R10, R11)
	STP	(R10, R11), (record_stack+64)(R19)
	LDP	80(R9), (R10, R11)
	STP	(R10, R11), (record_stack+80)(R19)
	LDP	96(R9), (R10, R11)
	STP	(R10, R11), (record_stack+96)(R19)
	LDP	112(R9), (R10, R11)
	STP	(R10, R11), (record_stack+112)(R19)
	MOVD	$·targets(SB), R9
	MOVD	(R9)(R16<<3), R10
	MOVD	R10, record_fn(R19)
	MOVD	$·targetFlags(SB), R9
	MOVD	(R9)(R16<<3), R20
	MOVD	R20, record_flags(R19)
	MOVD	ZR, record_next(R19)
	MOVD	ZR, record_sem(R19)

	MOVD	R19, R0
	CALL	·exchange(SB)

	TBZ	$1, R20, returned
	MOVD	record_r(R19), R0
	CBZ	R0, returned
	MOVD	$·link(SB), R9
	MOVD	linkState_autorelease(R9), R9
	CALL	R9
returned:
	LDP	(record_r)(R19), (R0, R1)
	FMOVD	(record_rd+0)(R19), F0
	FMOVD	(record_rd+8)(R19), F1
	FMOVD	(record_rd+16)(R19), F2
	FMOVD	(record_rd+24)(R19), F3
	LDP	(const_trampolineRecord+16)(RSP), (R19, R20)
	LDP	const_trampolineRecord(RSP), (R29, R30)
	ADD	$TFRAME, RSP
	RET

// func exchangeAddress() uintptr
TEXT ·exchangeAddress(SB), NOSPLIT, $0-8
	MOVD	$·exchange(SB), R0
	MOVD	R0, ret+0(FP)
	RET

// func drainQueueAddress() uintptr
TEXT ·drainQueueAddress(SB), NOSPLIT, $0-8
	MOVD	$·drainQueue(SB), R0
	MOVD	R0, ret+0(FP)
	RET

// func trampolineAddress(i uintptr) uintptr
TEXT ·trampolineAddress(SB), NOSPLIT, $0-16
	MOVD	i+0(FP), R1
	MOVD	$·trampolineTable(SB), R0
	ADD	R1<<3, R0
	MOVD	R0, ret+8(FP)
	RET

// invokeBlock(block) is a C function that calls a block that takes no
// arguments: the invoke function of the block, with the block.
TEXT ·invokeBlock(SB), NOSPLIT|NOFRAME, $0-0
	MOVD	const_blockInvoke(R0), R17
	JMP	(R17)

// runOnMain(block) is a C function. Called on the worker during a step, it
// hands the main thread a record that calls invokeBlock with the block and
// returns once the main thread ran it; anywhere else it calls invokeBlock
// at once.
TEXT ·runOnMain(SB), NOSPLIT|NOFRAME, $0-0
	MRS_TP_R17
	AND	$~7, R17
	MOVD	$·link(SB), R9
	MOVD	linkState_workerThread(R9), R10
	CMP	R10, R17
	BNE	now
	MOVD	linkState_depth(R9), R10
	CBZ	R10, now
	SUB	$TFRAME, RSP
	STP	(R29, R30), const_trampolineRecord(RSP)
	STP	(R19, R20), (const_trampolineRecord+16)(RSP)
	ADD	$const_trampolineRecord, RSP, R29
	MOVD	RSP, R19
	MOVD	R0, (record_x+0)(R19)
	MOVD	$·invokeBlock(SB), R10
	MOVD	R10, record_fn(R19)
	MOVD	ZR, record_flags(R19)
	MOVD	ZR, record_next(R19)
	MOVD	ZR, record_sem(R19)
	MOVD	R19, R0
	CALL	·exchange(SB)
	LDP	(const_trampolineRecord+16)(RSP), (R19, R20)
	LDP	const_trampolineRecord(RSP), (R29, R30)
	ADD	$TFRAME, RSP
	RET
now:
	JMP	·invokeBlock(SB)

// func invokeBlockAddress() uintptr
TEXT ·invokeBlockAddress(SB), NOSPLIT, $0-8
	MOVD	$·invokeBlock(SB), R0
	MOVD	R0, ret+0(FP)
	RET

// func runOnMainAddress() uintptr
TEXT ·runOnMainAddress(SB), NOSPLIT, $0-8
	MOVD	$·runOnMain(SB), R0
	MOVD	R0, ret+0(FP)
	RET
