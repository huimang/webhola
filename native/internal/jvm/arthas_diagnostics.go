package jvm

import (
	"context"
	"errors"
	"math"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	i18npkg "webhola/native/internal/i18n"
	platformpkg "webhola/native/internal/platform"
)

// Keep some class metadata capacity for the agent before injecting it. A
// system-wide memory percentage does not describe this per-JVM limit.
const arthasMetaspaceReserve = 16 << 20

var metaspaceUsedPattern = regexp.MustCompile(`(?m)Both:\s+([0-9.]+)\s+(B|KB|MB|GB) used\.`)

var metaspaceLimitPattern = regexp.MustCompile(`(?m)^MaxMetaspaceSize:\s+([0-9.]+)\s+(B|KB|MB|GB)\b`)

func metaspaceBytes(match []string) float64 {
	if len(match) != 3 {
		return 0
	}
	n, _ := strconv.ParseFloat(match[1], 64)
	switch match[2] {
	case "KB":
		n *= 1 << 10
	case "MB":
		n *= 1 << 20
	case "GB":
		n *= 1 << 30
	}
	return n
}

func arthasMetaspaceCheck(pid uint32, output string) error {
	used := metaspaceBytes(metaspaceUsedPattern.FindStringSubmatch(output))
	limit := metaspaceBytes(metaspaceLimitPattern.FindStringSubmatch(output))
	if used <= 0 || limit <= 0 || limit-used >= arthasMetaspaceReserve {
		return nil
	}
	// Leave at least 64 MiB beyond the current usage; suggest a rounded limit.
	suggested := max(256, int(math.Ceil((used/(1<<20)+64)/64))*64)
	return i18npkg.MessageError("arthas.insufficient_metaspace", pid, used/(1<<20), limit/(1<<20), suggested)
}

func checkArthasMetaspace(ctx context.Context, java string, pid uint32) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	name := "jcmd"
	if filepath.Ext(java) == ".exe" {
		name += ".exe"
	}
	cmd := exec.CommandContext(probeCtx, filepath.Join(filepath.Dir(java), name), strconv.FormatUint(uint64(pid), 10), "VM.metaspace", "basic")
	platformpkg.HideProcess(cmd)
	var out platformpkg.TailWriter
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		// Missing tools, permissions or unsupported JVMs must not block attach.
		return ctx.Err()
	}
	return arthasMetaspaceCheck(pid, string(out.Buf))
}

// Bootstrap can exit before the HTTP listener is ready. Only retry transport
// failures; PID mismatches, authentication and protocol errors are final.
func (c *ArthasClient) waitReady(ctx context.Context, timeout time.Duration) error {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		if readyCtx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if last != nil {
				return last
			}
			return readyCtx.Err()
		}
		last = c.Verify(readyCtx)
		if last == nil {
			return nil
		}
		var networkError net.Error
		if !errors.As(last, &networkError) {
			return last
		}
		select {
		case <-readyCtx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func arthasAttachFailure(ctx context.Context, java string, pid uint32, cause error, output string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := checkArthasMetaspace(ctx, java, pid); err != nil {
		return err
	}
	return i18npkg.MessageError("arthas.connection_failed_after_attach_w_s", cause, output)
}
