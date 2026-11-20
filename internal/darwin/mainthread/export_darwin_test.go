//go:build darwin

package mainthread

// UseFrameworkPrefix makes the sender of a closure pass over the functions of
// prefix instead of the framework until restore runs, so a check outside it
// can post through a function inside it.
func UseFrameworkPrefix(prefix string) (restore func()) {
	before := frameworkPrefix
	frameworkPrefix = prefix
	return func() { frameworkPrefix = before }
}

// SenderThroughFramework asks for the sender of a closure from a function of
// the framework, as a queue of the framework does when it posts.
func SenderThroughFramework() string { return ClosureSender() }
