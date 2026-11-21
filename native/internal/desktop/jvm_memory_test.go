package desktop

import (
	"context"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	discoverypkg "webhola/native/internal/discovery"
	jvmpkg "webhola/native/internal/jvm"
)

func TestParseJVMMemory(t *testing.T) {
	flags := "-XX:MaxHeapSize=268435456 -XX:MaxMetaspaceSize=134217728 "
	for _, tc := range []struct {
		name, heap      string
		used, committed float64
	}{
		{"G1", " garbage-first heap total 131072K, used 65536K [0x0]\n Metaspace used 12345K, committed 12544K, reserved 1114112K", 64, 128},
		{"parallel", " PSYoungGen total 32768K, used 16384K [0x0]\n ParOldGen total 98304K, used 32768K [0x0]\n Metaspace used 12345K, committed 12544K, reserved 1114112K", 48, 128},
		{"unknown collector", "Heap format unknown", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := jvmpkg.ParseJVMMemory(tc.heap, flags)
			if m.HeapUsed != tc.used*(1<<20) || m.HeapCommitted != tc.committed*(1<<20) || m.HeapMax != 256*(1<<20) || m.MetaMax != 128*(1<<20) {
				t.Fatalf("%+v", m)
			}
			if tc.used > 0 && (m.MetaUsed != 12345*1024 || m.MetaCommitted != 12544*1024) {
				t.Fatalf("%+v", m)
			}
		})
	}
	a := newApp(context.Background())
	if a.jvmMemoryValue(0, 0, 0) != "内存数据暂不可用" {
		t.Fatal("unknown data presented as zero")
	}
}

func TestJVMProcessPanel(t *testing.T) {
	a := newApp(context.Background())
	s := &arthasState{ctx: a.ctx, target: discoverypkg.Listener{PID: 10700, Process: "java.exe", Port: 8080, JVM: true}}
	s.memory = jvmpkg.JVMMemorySnapshot{Process: jvmpkg.ProcessSnapshot{State: "running", Path: `D:\apps\jdk\bin\java.exe`, Resident: 512 << 20, Private: 600 << 20}, HeapUsed: 64 << 20, HeapCommitted: 128 << 20, HeapMax: 256 << 20, MetaUsed: 127 << 20, MetaCommitted: 128 << 20, MetaMax: 128 << 20, Sampled: time.Now()}
	a.arthas = s
	a.workspace = 1
	tt := ui.NewTester(a.view, 1200, 820)
	for _, want := range []string{"java.exe · PID 10700", "运行中", "工作集 512.0 MiB", "Java 堆：64.0 MiB", "元空间：127.0 MiB"} {
		if !tt.HasText(want) {
			t.Fatal("missing " + want)
		}
	}
	if path := os.Getenv("WEBHOLA_JVM_MEMORY_PREVIEW"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	s.memory.Process.State = "exited"
	tt = ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("已退出") || !tt.HasText("内存数据已失效") || tt.HasText("工作集 512.0 MiB") {
		t.Fatal("exited process shows fresh memory")
	}
	s.memory.Process.State = "replaced"
	tt = ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("PID 已被复用") {
		t.Fatal("PID reuse not shown")
	}
	if !strings.Contains(a.jvmMemoryValue(1, 2, 0), "未获取到上限") {
		t.Fatal("unknown limit not labelled")
	}
}
