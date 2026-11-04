//go:build darwin && amd64

#include "textflag.h"
#include "go_asm.h"

// MOVQ %gs:0, AX: the pthread_t of the calling thread, which the thread's
// own word at the base of its gs segment holds.
#define GS0_AX BYTE $0x65; BYTE $0x48; BYTE $0x8b; BYTE $0x04; BYTE $0x25; LONG $0

// func threadPointer() uintptr
TEXT ·threadPointer(SB), NOSPLIT, $0-8
	GS0_AX
	MOVQ	AX, ret+0(FP)
	RET

// exchange(rec *record) is a C function. Called with a record, it hands the
// record to the other side, the worker from the main thread and the main
// thread from the worker, and returns once the other side handed it back
// done. Called with 0 on the worker, it hands nothing and returns after
// the step that sets link.stop. Meanwhile it runs each record that the
// other side hands it: on the worker as a step, in an autorelease pool of
// its own, and on the main thread as a forwarded call; the main thread
// also runs the records that other threads queue.
//
// The slots of its frame: the semaphore of its own side, that of the
// other side, the pool of a step, the record that it runs and the slot of
// the other side.
#define MYSEM 0
#define PEERSEM 8
#define POOL 16
#define MSG 24
#define PEERSLOT 32
TEXT ·exchange(SB), NOSPLIT|NOFRAME, $0-0
	PUSHQ	BP
	MOVQ	SP, BP
	PUSHQ	BX
	PUSHQ	R12
	PUSHQ	R13
	PUSHQ	R14
	SUBQ	$48, SP
	MOVQ	DI, BX
	MOVQ	$·link(SB), R14

	GS0_AX
	CMPQ	AX, linkState_mainThread(R14)
	JNE	worker
	MOVQ	$1, R12
	LEAQ	linkState_toMain(R14), R13
	LEAQ	linkState_toWorker(R14), AX
	MOVQ	AX, PEERSLOT(SP)
	MOVQ	linkState_mainSem(R14), AX
	MOVQ	AX, MYSEM(SP)
	MOVQ	linkState_workerSem(R14), AX
	MOVQ	AX, PEERSEM(SP)
	JMP	start
worker:
	XORQ	R12, R12
	LEAQ	linkState_toWorker(R14), R13
	LEAQ	linkState_toMain(R14), AX
	MOVQ	AX, PEERSLOT(SP)
	MOVQ	linkState_workerSem(R14), AX
	MOVQ	AX, MYSEM(SP)
	MOVQ	linkState_mainSem(R14), AX
	MOVQ	AX, PEERSEM(SP)

start:
	TESTQ	BX, BX
	JZ	loop
	MOVQ	PEERSLOT(SP), CX
	MOVQ	BX, DX
	MOVQ	DX, AX
	XCHGQ	AX, (CX)
	TESTQ	AX, AX
	JNZ	full
	MOVL	PEERSEM(SP), DI
	MOVQ	linkState_semaphoreSignal(R14), AX
	CALL	AX

loop:
	TESTQ	R12, R12
	JZ	take
	XORL	DI, DI
	CALL	·drainQueue(SB)
take:
	XORL	AX, AX
	XCHGQ	AX, (R13)
	TESTQ	AX, AX
	JNZ	got
	MOVL	MYSEM(SP), DI
	MOVQ	linkState_semaphoreWait(R14), AX
	CALL	AX
	JMP	loop

got:
	BTQ	$0, AX
	JCC	runit
	ANDQ	$~1, AX
	CMPQ	AX, BX
	JEQ	done
	// The other side handed back a record that this side did not hand it.
	MOVQ	AX, CX
	MOVQ	BX, DX
	MOVQ	$1, DI
	MOVQ	R12, SI
	MOVQ	linkState_violation(R14), AX
	CALL	AX
	BYTE	$0x0f; BYTE $0x0b // UD2

runit:
	MOVQ	AX, MSG(SP)
	TESTQ	R12, R12
	JNZ	onmain
	// A step on the worker.
	INCQ	linkState_depth(R14)
	MOVQ	linkState_poolPush(R14), AX
	CALL	AX
	MOVQ	AX, POOL(SP)
	MOVQ	MSG(SP), DI
	CALL	·callRecord(SB)
	// The pool of the worker does not take the object that the step
	// returns: the main thread autoreleases it in its own pool.
	MOVQ	MSG(SP), AX
	MOVQ	record_flags(AX), CX
	BTQ	$1, CX
	JCC	pop
	MOVQ	record_r(AX), DI
	TESTQ	DI, DI
	JZ	pop
	MOVQ	linkState_retain(R14), AX
	CALL	AX
pop:
	MOVQ	POOL(SP), DI
	MOVQ	linkState_poolPop(R14), AX
	CALL	AX
	DECQ	linkState_depth(R14)
	JMP	post
onmain:
	MOVQ	MSG(SP), DI
	CALL	·callRecord(SB)

post:
	MOVQ	MSG(SP), DX
	ORQ	$1, DX
	MOVQ	PEERSLOT(SP), CX
	MOVQ	DX, AX
	XCHGQ	AX, (CX)
	TESTQ	AX, AX
	JNZ	full
	MOVL	PEERSEM(SP), DI
	MOVQ	linkState_semaphoreSignal(R14), AX
	CALL	AX
	TESTQ	BX, BX
	JNZ	loop
	CMPQ	linkState_stop(R14), $0
	JEQ	loop

done:
	ADDQ	$48, SP
	POPQ	R14
	POPQ	R13
	POPQ	R12
	POPQ	BX
	POPQ	BP
	RET

full:
	// The slot of the other side held AX when this side handed it DX.
	MOVQ	AX, CX
	MOVQ	$2, DI
	MOVQ	R12, SI
	MOVQ	linkState_violation(R14), AX
	CALL	AX
	BYTE	$0x0f; BYTE $0x0b // UD2

// drainQueue(info uintptr) is a C function, the perform function of the
// run loop source of the main thread: it runs each record that threads
// other than the worker queued and signals the semaphore of each.
TEXT ·drainQueue(SB), NOSPLIT|NOFRAME, $0-0
	PUSHQ	BP
	MOVQ	SP, BP
	PUSHQ	BX
	PUSHQ	R12
	XORL	BX, BX
	MOVQ	$·link(SB), AX
	XCHGQ	BX, linkState_queue(AX)
next:
	TESTQ	BX, BX
	JZ	out
	MOVQ	record_next(BX), R12
	MOVQ	BX, DI
	CALL	·callRecord(SB)
	MOVL	record_sem(BX), DI
	MOVQ	$·link(SB), AX
	MOVQ	linkState_semaphoreSignal(AX), AX
	CALL	AX
	MOVQ	R12, BX
	JMP	next
out:
	POPQ	R12
	POPQ	BX
	POPQ	BP
	RET

// The frame of a trampoline on the main thread: its record, then the
// registers it saves.
#define TFRAME (const_trampolineRecord+16)
#define SAVED const_trampolineRecord

// trampolineCommon is where entry R11 of trampolineTable branches. Off the
// main thread, or while the worker does not run, it branches to the C
// function of the entry with each argument register as it came, al among
// them, which it keeps in R10 meanwhile. On the
// main thread it stores the argument registers and the stack arguments in
// a record on its stack, hands the record to the worker as a step and
// returns the result that the worker stores in it.
TEXT ·trampolineCommon(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ	AX, R10
	GS0_AX
	CMPQ	AX, ·link+linkState_mainThread(SB)
	JNE	direct
	CMPQ	·link+linkState_running(SB), $0
	JEQ	direct

	PUSHQ	BP
	MOVQ	SP, BP
	SUBQ	$TFRAME, SP
	MOVQ	BX, SAVED(SP)
	MOVQ	R12, (SAVED+8)(SP)
	MOVQ	SP, BX
	MOVQ	R11, R12
	MOVQ	DI, (record_x+0)(BX)
	MOVQ	SI, (record_x+8)(BX)
	MOVQ	DX, (record_x+16)(BX)
	MOVQ	CX, (record_x+24)(BX)
	MOVQ	R8, (record_x+32)(BX)
	MOVQ	R9, (record_x+40)(BX)
	MOVQ	R10, record_al(BX)
	MOVSD	X0, (record_xmm+0)(BX)
	MOVSD	X1, (record_xmm+8)(BX)
	MOVSD	X2, (record_xmm+16)(BX)
	MOVSD	X3, (record_xmm+24)(BX)
	MOVSD	X4, (record_xmm+32)(BX)
	MOVSD	X5, (record_xmm+40)(BX)
	MOVSD	X6, (record_xmm+48)(BX)
	MOVSD	X7, (record_xmm+56)(BX)
	LEAQ	16(BP), R11
	MOVQ	0(R11), AX
	MOVQ	AX, (record_stack+0)(BX)
	MOVQ	8(R11), AX
	MOVQ	AX, (record_stack+8)(BX)
	MOVQ	16(R11), AX
	MOVQ	AX, (record_stack+16)(BX)
	MOVQ	24(R11), AX
	MOVQ	AX, (record_stack+24)(BX)
	MOVQ	32(R11), AX
	MOVQ	AX, (record_stack+32)(BX)
	MOVQ	40(R11), AX
	MOVQ	AX, (record_stack+40)(BX)
	MOVQ	48(R11), AX
	MOVQ	AX, (record_stack+48)(BX)
	MOVQ	56(R11), AX
	MOVQ	AX, (record_stack+56)(BX)
	MOVQ	64(R11), AX
	MOVQ	AX, (record_stack+64)(BX)
	MOVQ	72(R11), AX
	MOVQ	AX, (record_stack+72)(BX)
	MOVQ	80(R11), AX
	MOVQ	AX, (record_stack+80)(BX)
	MOVQ	88(R11), AX
	MOVQ	AX, (record_stack+88)(BX)
	MOVQ	96(R11), AX
	MOVQ	AX, (record_stack+96)(BX)
	MOVQ	104(R11), AX
	MOVQ	AX, (record_stack+104)(BX)
	MOVQ	112(R11), AX
	MOVQ	AX, (record_stack+112)(BX)
	MOVQ	120(R11), AX
	MOVQ	AX, (record_stack+120)(BX)
	MOVQ	$·targets(SB), R11
	MOVQ	(R11)(R12*8), AX
	MOVQ	AX, record_fn(BX)
	MOVQ	$·targetFlags(SB), R11
	MOVQ	(R11)(R12*8), R12
	MOVQ	R12, record_flags(BX)
	MOVQ	$0, record_next(BX)
	MOVQ	$0, record_sem(BX)

	MOVQ	BX, DI
	CALL	·exchange(SB)

	BTQ	$1, R12
	JCC	returned
	MOVQ	(record_r+0)(BX), DI
	TESTQ	DI, DI
	JZ	returned
	MOVQ	$·link(SB), AX
	MOVQ	linkState_autorelease(AX), AX
	CALL	AX
returned:
	MOVQ	(record_r+0)(BX), AX
	MOVQ	(record_r+8)(BX), DX
	MOVSD	(record_rx+0)(BX), X0
	MOVSD	(record_rx+8)(BX), X1
	MOVQ	(SAVED+8)(SP), R12
	MOVQ	SAVED(SP), BX
	ADDQ	$TFRAME, SP
	POPQ	BP
	RET

direct:
	MOVQ	$·targets(SB), AX
	MOVQ	(AX)(R11*8), R11
	MOVQ	R10, AX
	JMP	R11

// runApp(records *[4]*record) is a C function, which the main thread calls
// to run an application with no Go code of its own in between: it hands
// the worker the first record, a step, and unless that step returned
// other than 0, makes the call of the second, the event loop, which it
// reads once that step returned, when it has a function, and hands the
// worker the third, a step; then it hands the worker the fourth, the step
// that ends it.
TEXT ·runApp(SB), NOSPLIT|NOFRAME, $0-0
	PUSHQ	BP
	MOVQ	SP, BP
	PUSHQ	BX
	SUBQ	$8, SP
	MOVQ	DI, BX
	MOVQ	0(BX), DI
	CALL	·exchange(SB)
	MOVQ	0(BX), AX
	CMPQ	(record_r+0)(AX), $0
	JNE	end
	MOVQ	8(BX), DI
	CMPQ	record_fn(DI), $0
	JEQ	finish
	CALL	·callRecord(SB)
finish:
	MOVQ	16(BX), DI
	CALL	·exchange(SB)
end:
	MOVQ	24(BX), DI
	CALL	·exchange(SB)
	ADDQ	$8, SP
	POPQ	BX
	POPQ	BP
	RET

// func runAppAddress() uintptr
TEXT ·runAppAddress(SB), NOSPLIT, $0-8
	MOVQ	$·runApp(SB), AX
	MOVQ	AX, ret+0(FP)
	RET

// func exchangeAddress() uintptr
TEXT ·exchangeAddress(SB), NOSPLIT, $0-8
	MOVQ	$·exchange(SB), AX
	MOVQ	AX, ret+0(FP)
	RET

// func drainQueueAddress() uintptr
TEXT ·drainQueueAddress(SB), NOSPLIT, $0-8
	MOVQ	$·drainQueue(SB), AX
	MOVQ	AX, ret+0(FP)
	RET

// func trampolineAddress(i uintptr) uintptr: the entries of trampolineTable
// lie 16 bytes apart.
TEXT ·trampolineAddress(SB), NOSPLIT, $0-16
	MOVQ	i+0(FP), CX
	SHLQ	$4, CX
	MOVQ	$·trampolineTable(SB), AX
	ADDQ	CX, AX
	MOVQ	AX, ret+8(FP)
	RET

// invokeBlock(block) is a C function that calls a block that takes no
// arguments: the invoke function of the block, with the block.
TEXT ·invokeBlock(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ	const_blockInvoke(DI), R11
	JMP	R11

// runOnMain(block) is a C function. Called on the worker during a step, it
// hands the main thread a record that calls invokeBlock with the block and
// returns once the main thread ran it; anywhere else it calls invokeBlock
// at once.
TEXT ·runOnMain(SB), NOSPLIT|NOFRAME, $0-0
	GS0_AX
	CMPQ	AX, ·link+linkState_workerThread(SB)
	JNE	now
	CMPQ	·link+linkState_depth(SB), $0
	JEQ	now
	PUSHQ	BP
	MOVQ	SP, BP
	SUBQ	$TFRAME, SP
	MOVQ	BX, SAVED(SP)
	MOVQ	SP, BX
	MOVQ	DI, (record_x+0)(BX)
	MOVQ	$0, record_al(BX)
	MOVQ	$·invokeBlock(SB), AX
	MOVQ	AX, record_fn(BX)
	MOVQ	$0, record_flags(BX)
	MOVQ	$0, record_next(BX)
	MOVQ	$0, record_sem(BX)
	MOVQ	BX, DI
	CALL	·exchange(SB)
	MOVQ	SAVED(SP), BX
	ADDQ	$TFRAME, SP
	POPQ	BP
	RET
now:
	JMP	·invokeBlock(SB)

// func invokeBlockAddress() uintptr
TEXT ·invokeBlockAddress(SB), NOSPLIT, $0-8
	MOVQ	$·invokeBlock(SB), AX
	MOVQ	AX, ret+0(FP)
	RET

// func runOnMainAddress() uintptr
TEXT ·runOnMainAddress(SB), NOSPLIT, $0-8
	MOVQ	$·runOnMain(SB), AX
	MOVQ	AX, ret+0(FP)
	RET
