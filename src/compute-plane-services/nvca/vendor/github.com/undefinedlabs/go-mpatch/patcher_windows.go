//go:build windows
// +build windows

package mpatch

import (
	"syscall"
	"unsafe"
)

const pageExecuteReadAndWrite = 0x40

var virtualProtectProc = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualProtect")

func callVirtualProtect(lpAddress unsafe.Pointer, dwSize int, flNewProtect uint32, lpflOldProtect unsafe.Pointer) error {
	ret, _, _ := virtualProtectProc.Call(uintptr(lpAddress), uintptr(dwSize), uintptr(flNewProtect), uintptr(lpflOldProtect))
	if ret == 0 {
		return syscall.GetLastError()
	}
	return nil
}

func writeDataToPointer(ptr unsafe.Pointer, data []byte) error {
	var oldPerms, tmp uint32
	dataLength := len(data)
	ptrByteSlice := getMemorySliceFromPointer(ptr, len(data))
	if err := callVirtualProtect(ptr, dataLength, pageExecuteReadAndWrite, unsafe.Pointer(&oldPerms)); err != nil {
		return err
	}
	copy(ptrByteSlice, data[:])
	if err := callVirtualProtect(ptr, dataLength, oldPerms, unsafe.Pointer(&tmp)); err != nil {
		return err
	}
	return nil
}
