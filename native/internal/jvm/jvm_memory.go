package jvm

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	i18npkg "webhola/native/internal/i18n"
	platformpkg "webhola/native/internal/platform"
)

type ProcessSnapshot struct {
	State, Path              string
	Birth, Resident, Private uint64
}

type JVMMemorySnapshot struct {
	Process                          ProcessSnapshot
	HeapUsed, HeapCommitted, HeapMax float64
	MetaUsed, MetaCommitted, MetaMax float64
	Sampled                          time.Time
	Note                             string
}

var jvmHeapPattern = regexp.MustCompile(`(?mi)^\s*(?:garbage-first heap|PSYoungGen|ParOldGen|PSOldGen|def new generation|tenured generation|par new generation|concurrent mark-sweep generation|Shenandoah Heap|ZHeap)\s+total\s+([0-9.]+)([KMG]?),\s+used\s+([0-9.]+)([KMG]?)`)

var jvmMetaPattern = regexp.MustCompile(`(?mi)^\s*Metaspace\s+used\s+([0-9.]+)([KMG]?),\s+committed\s+([0-9.]+)([KMG]?)`)

var jvmHeapMaxPattern = regexp.MustCompile(`(?:^|\s)-XX:MaxHeapSize=([0-9]+)(?:\s|$)`)

var jvmMetaMaxPattern = regexp.MustCompile(`(?:^|\s)-XX:MaxMetaspaceSize=([0-9]+)(?:\s|$)`)

func jvmSize(n, unit string) float64 {
	value, _ := strconv.ParseFloat(n, 64)
	switch unit {
	case "K":
		value *= 1 << 10
	case "M":
		value *= 1 << 20
	case "G":
		value *= 1 << 30
	}
	return value
}

func ParseJVMMemory(heap, flags string) JVMMemorySnapshot {
	var m JVMMemorySnapshot
	for _, v := range jvmHeapPattern.FindAllStringSubmatch(heap, -1) {
		m.HeapCommitted += jvmSize(v[1], v[2])
		m.HeapUsed += jvmSize(v[3], v[4])
	}
	if v := jvmMetaPattern.FindStringSubmatch(heap); len(v) > 0 {
		m.MetaUsed = jvmSize(v[1], v[2])
		m.MetaCommitted = jvmSize(v[3], v[4])
	}
	if v := jvmHeapMaxPattern.FindStringSubmatch(flags); len(v) > 0 {
		m.HeapMax = jvmSize(v[1], "")
	}
	if v := jvmMetaMaxPattern.FindStringSubmatch(flags); len(v) > 0 {
		m.MetaMax = jvmSize(v[1], "")
	}
	return m
}

func runJVMDiagnostic(ctx context.Context, java string, pid uint32, command string) (string, error) {
	name := "jcmd"
	if strings.EqualFold(filepath.Ext(java), ".exe") {
		name += ".exe"
	}
	cmd := exec.CommandContext(ctx, filepath.Join(filepath.Dir(java), name), strconv.FormatUint(uint64(pid), 10), command)
	platformpkg.HideProcess(cmd)
	var out platformpkg.TailWriter
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return string(out.Buf), err
}

func SampleJVMMemory(ctx context.Context, cfg ArthasConfig, pid uint32, birth uint64) JVMMemorySnapshot {
	p := ReadJVMProcess(pid)
	m := JVMMemorySnapshot{Process: p, Sampled: time.Now()}
	if birth != 0 && p.Birth != 0 && birth != p.Birth {
		m.Process.State = "replaced"
		return m
	}
	if p.State != "running" {
		return m
	}
	java, err := cfg.Java()
	if err != nil {
		m.Note = i18npkg.MessageText("jvm_memory.unavailable")
		return m
	}
	heap, heapErr := runJVMDiagnostic(ctx, java, pid, "GC.heap_info")
	flags, flagsErr := runJVMDiagnostic(ctx, java, pid, "VM.flags")
	m = ParseJVMMemory(heap, flags)
	m.Process = p
	m.Sampled = time.Now()
	if heapErr != nil || flagsErr != nil || m.HeapCommitted == 0 || m.MetaCommitted == 0 {
		m.Note = i18npkg.MessageText("jvm_memory.unavailable")
	}
	// Recheck after diagnostics so a process that exited during collection is
	// never presented as running with fresh memory readings.
	latest := ReadJVMProcess(pid)
	if latest.State != "running" {
		m.Process.State = latest.State
	} else if p.Birth != 0 && latest.Birth != 0 && p.Birth != latest.Birth {
		m.Process.State = "replaced"
	}
	return m
}

func MemoryMiB(n float64) string { return fmt.Sprintf("%.1f MiB", n/(1<<20)) }
