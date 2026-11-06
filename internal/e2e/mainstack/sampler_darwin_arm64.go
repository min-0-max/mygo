//go:build darwin && arm64

package mainstack

// samplerLoop is the start routine of the thread that samples the stack
// pointer of the main thread (sampler_darwin_arm64.s). It is a C function,
// which pthread_create starts with the address of the sampler's state, and
// Go never calls it.
func samplerLoop()

// samplerLoopAddress returns the address of samplerLoop, for
// pthread_create.
func samplerLoopAddress() uintptr
