//go:build amd64
// +build amd64

package mpatch

import "unsafe"

// Gets the jump function rewrite bytes
//
//go:nosplit
func getJumpFuncBytes(to unsafe.Pointer) ([]byte, error) {
	return []byte{
		0x48, 0xBA,
		byte(uintptr(to)),
		byte(uintptr(to) >> 8),
		byte(uintptr(to) >> 16),
		byte(uintptr(to) >> 24),
		byte(uintptr(to) >> 32),
		byte(uintptr(to) >> 40),
		byte(uintptr(to) >> 48),
		byte(uintptr(to) >> 56),
		0xFF, 0x22,
	}, nil
}
