//go:build !darwin && !js

package e2e

// The activation tests are macOS's: no helper process runs elsewhere.
func activationApp(string) {}
