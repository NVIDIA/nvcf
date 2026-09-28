//go:build !windows
// +build !windows

package mpatch

import (
	"reflect"
	"syscall"
	"time"
	"unsafe"
)

var writeAccess = syscall.PROT_READ | syscall.PROT_WRITE | syscall.PROT_EXEC
var readAccess = syscall.PROT_READ | syscall.PROT_EXEC

//go:nosplit
func getMemorySliceFromUintptr(p uintptr, length int) []byte {
	return *(*[]byte)(unsafe.Pointer(&reflect.SliceHeader{
		Data: p,
		Len:  length,
		Cap:  length,
	}))
}

//go:nosplit
func callMProtect(addr unsafe.Pointer, length int, prot int) error {
	for p := uintptr(addr) & ^(uintptr(pageSize - 1)); p < uintptr(addr)+uintptr(length); p += uintptr(pageSize) {
		page := getMemorySliceFromUintptr(p, pageSize)
		if err := syscall.Mprotect(page, prot); err != nil {
			return err
		}
	}
	return nil
}

func writeDataToPointer(ptr unsafe.Pointer, data []byte) error {
	dataLength := len(data)
	ptrByteSlice := getMemorySliceFromPointer(ptr, len(data))
	if err := callMProtect(ptr, dataLength, writeAccess); err != nil {
		return err
	}
	copy(ptrByteSlice, data[:])
	if err := callMProtect(ptr, dataLength, readAccess); err != nil {
		return err
	}
	<-time.After(100 * time.Microsecond) // If we remove this line then it fails to unpatch on ARM64
	return nil
}
