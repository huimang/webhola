package jvm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	platformpkg "webhola/native/internal/platform"
)

func TestJVMProcessHelper(t *testing.T) {
	if os.Getenv("WEBHOLA_PROCESS_TEST_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestJVMProcessLivenessAndReuse(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestJVMProcessHelper$")
	platformpkg.HideProcess(cmd)
	cmd.Env = append(os.Environ(), "WEBHOLA_PROCESS_TEST_HELPER=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if _, err := bufio.NewReader(output).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	pid := uint32(cmd.Process.Pid)
	p := ReadJVMProcess(pid)
	if p.State != "running" || p.Birth == 0 || p.Resident == 0 || p.Private == 0 || p.Path == "" {
		t.Fatalf("%+v", p)
	}
	m := SampleJVMMemory(context.Background(), ArthasConfig{}, pid, p.Birth+1)
	if m.Process.State != "replaced" || m.HeapUsed != 0 {
		t.Fatalf("PID reuse not rejected: %+v", m)
	}
	input.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if p := ReadJVMProcess(pid); p.State != "exited" {
		t.Fatalf("exited process reported as %+v", p)
	}
}
