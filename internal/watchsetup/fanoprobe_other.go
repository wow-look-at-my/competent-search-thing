//go:build !linux

package watchsetup

// probeFanotifySupport is a no-op off Linux: fanotify does not exist, so
// there is nothing to grant. (Ensure/Attempt already return early for a
// non-Linux GOOS; this keeps the package building on every target.)
func probeFanotifySupport() State { return StateUnsupported }
