//go:build !windows

package library

import "testing"

// processReadBytes is how many bytes this process has read so far. Only
// Windows tells, and the test that asks skips itself elsewhere.
func processReadBytes(t *testing.T) uint64 {
	t.Skip("only Windows counts what a process reads")
	return 0
}
