//go:build 386
// +build 386

package mpatch

import "unsafe"

// Gets the jump function rewrite bytes
//
//go:nosplit
func getJumpFuncBytes(to unsafe.Pointer) ([]byte, error) {
	return []byte{
		0xBA,
		byte(uintptr(to)),
		byte(uintptr(to) >> 8),
		byte(uintptr(to) >> 16),
		byte(uintptr(to) >> 24),
		0xFF, 0x22,
	}, nil
}
