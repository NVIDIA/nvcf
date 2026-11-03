//go:build arm64
// +build arm64

package mpatch

import "unsafe"

// Code from: https://github.com/agiledragon/gomonkey/blob/master/jmp_arm64.go

// Gets the jump function rewrite bytes
//
//go:nosplit
func getJumpFuncBytes(to unsafe.Pointer) ([]byte, error) {
	res := make([]byte, 0, 24)
	d0d1 := uintptr(to) & 0xFFFF
	d2d3 := uintptr(to) >> 16 & 0xFFFF
	d4d5 := uintptr(to) >> 32 & 0xFFFF
	d6d7 := uintptr(to) >> 48 & 0xFFFF

	res = append(res, movImm(0b10, 0, d0d1)...)          // MOVZ x26, double[16:0]
	res = append(res, movImm(0b11, 1, d2d3)...)          // MOVK x26, double[32:16]
	res = append(res, movImm(0b11, 2, d4d5)...)          // MOVK x26, double[48:32]
	res = append(res, movImm(0b11, 3, d6d7)...)          // MOVK x26, double[64:48]
	res = append(res, []byte{0x4A, 0x03, 0x40, 0xF9}...) // LDR x10, [x26]
	res = append(res, []byte{0x40, 0x01, 0x1F, 0xD6}...) // BR x10

	return res, nil
}

func movImm(opc, shift int, val uintptr) []byte {
	var m uint32 = 26          // rd
	m |= uint32(val) << 5      // imm16
	m |= uint32(shift&3) << 21 // hw
	m |= 0b100101 << 23        // const
	m |= uint32(opc&0x3) << 29 // opc
	m |= 0b1 << 31             // sf

	res := make([]byte, 4)
	*(*uint32)(unsafe.Pointer(&res[0])) = m

	return res
}
