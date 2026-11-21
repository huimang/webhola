//go:build !windows

package jvm

func ReadJVMProcess(uint32) ProcessSnapshot { return ProcessSnapshot{State: "unknown"} }
