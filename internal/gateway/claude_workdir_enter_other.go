//go:build !windows

package gateway

import "syscall"

// canEnter says the folder is one magpie's process may enter, as a run
// working there would have to (chdir asks the same leave as access does;
// 0o1 is X_OK, which syscall has no name for on darwin).
func canEnter(dir string) bool { return syscall.Access(dir, 0o1) == nil }
