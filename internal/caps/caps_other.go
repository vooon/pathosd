//go:build !linux

package caps

// RaiseEffective is a no-op outside Linux.
func RaiseEffective() (bool, error) { return false, nil }
