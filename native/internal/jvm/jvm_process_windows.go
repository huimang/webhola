package jvm

import (
	"errors"
	"syscall"
	"unsafe"

	discoverypkg "webhola/native/internal/discovery"
)

var getProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCounters struct {
	Size, PageFaultCount                               uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage, PrivateUsage     uintptr
}

func ReadJVMProcess(pid uint32) ProcessSnapshot {
	p := ProcessSnapshot{State: "unknown"}
	h, err := syscall.OpenProcess(0x1000|0x0010, false, pid)
	if err != nil {
		h, err = syscall.OpenProcess(0x1000, false, pid)
	}
	if err != nil {
		if errors.Is(err, syscall.Errno(87)) {
			p.State = "exited"
		}
		return p
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return p
	}
	if code != 259 {
		p.State = "exited"
		return p
	}
	p.State = "running"
	var created, exited, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
		p.Birth = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	}
	var path [32768]uint16
	size := uint32(len(path))
	if ok, _, _ := discoverypkg.QueryProcessName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&size))); ok != 0 {
		p.Path = syscall.UTF16ToString(path[:size])
	}
	var m processMemoryCounters
	m.Size = uint32(unsafe.Sizeof(m))
	if ok, _, _ := getProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.Size)); ok != 0 {
		p.Resident, p.Private = uint64(m.WorkingSetSize), uint64(m.PrivateUsage)
	}
	return p
}
