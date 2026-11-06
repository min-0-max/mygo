//go:build darwin && arm64

#include "textflag.h"

// The sampler's state is an array of 64-bit words, the layout of which
// sampler_darwin_arm64_test.go defines with the same offsets.
#define PORT 0
#define LOW 8
#define HIGH 16
#define STOP 24
#define PHASE 32
#define SUSPEND 40
#define GETSTATE 48
#define RESUME 56
#define USLEEP 64
#define FAILURES 72
#define LASTERROR 80
#define COUNT 88
#define SAMPLES 96
#define PHASES 16
#define OUTSIDE (SAMPLES+8*PHASES)
#define FIRST (OUTSIDE+8*PHASES)
#define REGS (FIRST+8*PHASES)
#define FIRSTPC (REGS+272)
// The stack pointer in an arm_thread_state64_t, after x0 to x28, fp and
// lr, and the program counter after it.
#define SP_IN_STATE 248
#define PC_IN_STATE 256
// ARM_THREAD_STATE64 and ARM_THREAD_STATE64_COUNT.
#define FLAVOR 6
#define STATECOUNT 68
// The interval between two samples, in microseconds.
#define INTERVAL 20

// samplerLoop(state *[...]uint64) is a C function that a thread started
// with pthread_create runs. While a phase is set, it suspends the main
// thread with thread_suspend, reads its registers with thread_get_state,
// resumes it with thread_resume and counts the sample for the phase, and
// counts it as outside when the stack pointer lies outside the stack of
// the main thread, keeping the stack pointer and the program counter of
// the first; it then sleeps with usleep. It calls those functions
// through the addresses in the state, which the test resolves before the
// thread starts, so that while the main thread is suspended it takes no
// lock, allocates nothing and runs no Go code: the main thread may hold
// the lock of the allocator when it is suspended. It returns when the stop
// word is set.
TEXT ·samplerLoop(SB), NOSPLIT|NOFRAME, $0-0
	SUB	$80, RSP
	STP	(R29, R30), 0(RSP)
	STP	(R19, R20), 16(RSP)
	STP	(R21, R22), 32(RSP)
	STP	(R23, R27), 48(RSP)
	MOVD	RSP, R29
	MOVD	R0, R19

loop:
	ADD	$STOP, R19, R1
	LDAR	(R1), R2
	CBNZ	R2, done
	ADD	$PHASE, R19, R1
	LDAR	(R1), R20
	CMP	$0, R20
	BLT	sleep
	CMP	$PHASES, R20
	BGE	sleep

	MOVWU	PORT(R19), R0
	MOVD	SUSPEND(R19), R1
	CALL	R1
	CBZW	R0, suspended
	MOVW	R0, R21
	B	failed

suspended:
	MOVD	$STATECOUNT, R1
	MOVW	R1, COUNT(R19)
	MOVWU	PORT(R19), R0
	MOVD	$FLAVOR, R1
	ADD	$REGS, R19, R2
	ADD	$COUNT, R19, R3
	MOVD	GETSTATE(R19), R4
	CALL	R4
	MOVW	R0, R21
	MOVWU	PORT(R19), R0
	MOVD	RESUME(R19), R1
	CALL	R1
	MOVW	R0, R22
	CBNZW	R21, failed

	// samples[phase]++
	LSL	$3, R20, R23
	ADD	R19, R23, R23
	MOVD	SAMPLES(R23), R2
	ADD	$1, R2
	MOVD	R2, SAMPLES(R23)
	// The stack pointer against [low, high].
	ADD	$REGS, R19, R1
	MOVD	SP_IN_STATE(R1), R2
	MOVD	LOW(R19), R3
	CMP	R3, R2
	BLO	outside
	MOVD	HIGH(R19), R3
	CMP	R3, R2
	BHI	outside
	B	resumed

outside:
	MOVD	OUTSIDE(R23), R3
	CBNZ	R3, counted
	MOVD	R2, FIRST(R23)
	MOVD	PC_IN_STATE(R1), R2
	MOVD	R2, FIRSTPC(R23)
counted:
	ADD	$1, R3
	MOVD	R3, OUTSIDE(R23)

resumed:
	CBZW	R22, sleep
	MOVW	R22, R21

failed:
	MOVD	FAILURES(R19), R2
	ADD	$1, R2
	MOVD	R2, FAILURES(R19)
	MOVW	R21, R2
	MOVD	R2, LASTERROR(R19)

sleep:
	MOVD	$INTERVAL, R0
	MOVD	USLEEP(R19), R1
	CALL	R1
	B	loop

done:
	MOVD	$0, R0
	LDP	48(RSP), (R23, R27)
	LDP	32(RSP), (R21, R22)
	LDP	16(RSP), (R19, R20)
	LDP	0(RSP), (R29, R30)
	ADD	$80, RSP
	RET

// func samplerLoopAddress() uintptr
TEXT ·samplerLoopAddress(SB), NOSPLIT, $0-8
	MOVD	$·samplerLoop(SB), R0
	MOVD	R0, ret+0(FP)
	RET
