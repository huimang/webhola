package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
	platformpkg "webhola/native/internal/platform"
)

// Exercises the installer-provided full distribution without Spring or a
// bootstrap dependency download. Only the process created by this test is attached.
func TestArthasFullDistributionIntegration(t *testing.T) {
	if os.Getenv("WEBHOLA_ARTHAS_FULL_INTEGRATION") != "1" {
		t.Skip("requires an installed JDK and full Arthas distribution")
	}
	cfg := jvmpkg.ArthasConfig{JavaPath: os.Getenv("WEBHOLA_JAVA"), BootPath: os.Getenv("WEBHOLA_ARTHAS_BOOT")}
	dir := t.TempDir()
	source := filepath.Join(dir, "OfflineFixture.java")
	if err := os.WriteFile(source, []byte(`import java.net.*;
public class OfflineFixture {
 public static String describe(String personName, int repeatCount) { return personName.repeat(repeatCount); }
 public static String describe(int number) { return Integer.toString(number); }
 public static void main(String[] args) throws Exception {
  try (ServerSocket socket = new ServerSocket(0, 1, InetAddress.getLoopbackAddress())) {
   System.out.println(socket.getLocalPort()); System.out.flush(); Thread.sleep(120000);
  }
 }
}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	compile := exec.CommandContext(ctx, filepath.Join(filepath.Dir(cfg.JavaPath), "javac.exe"), "-g", source)
	platformpkg.HideProcess(compile)
	if b, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("javac: %v %s", err, b)
	}
	cmd := exec.CommandContext(ctx, cfg.JavaPath, "-Duser.home="+dir, "-cp", dir, "OfflineFixture")
	cmd.Stderr = os.Stderr
	platformpkg.HideProcess(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	listeners, err := discoverypkg.ListListeners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolveJVMTarget(listeners, port, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.HTTPPort = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	memory := jvmpkg.SampleJVMMemory(ctx, cfg, target.PID, 0)
	if memory.Process.State != "running" || memory.HeapCommitted == 0 || memory.HeapMax == 0 || memory.MetaCommitted == 0 || memory.Note != "" {
		t.Fatalf("live JVM memory collection failed: %+v", memory)
	}
	// A small independent JVM must be rejected before the agent is injected.
	low := exec.CommandContext(ctx, cfg.JavaPath, "-XX:MaxMetaspaceSize=16m", "-cp", dir, "OfflineFixture")
	platformpkg.HideProcess(low)
	lowOut, err := low.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := low.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = low.Process.Kill(); _ = low.Wait() }()
	if _, err := bufio.NewReader(lowOut).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	lowClient, lowErr := jvmpkg.AttachArthas(ctx, cfg, discoverypkg.Listener{PID: uint32(low.Process.Pid), JVM: true})
	if lowClient != nil || lowErr == nil || !strings.Contains(i18npkg.TranslateText("en", i18npkg.ErrorMessage(lowErr)), "less than 16 MiB") {
		t.Fatalf("low metaspace JVM was not protected: %v", lowErr)
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+cfg.HTTPPort, time.Second); err == nil {
		conn.Close()
		t.Fatal("low metaspace check unexpectedly started an agent listener")
	}
	c, err := jvmpkg.AttachArthas(ctx, cfg, target)
	if err != nil {
		if b, e := os.ReadFile(filepath.Join(dir, "logs", "arthas", "arthas.log")); e == nil {
			t.Log(string(b))
		}
		t.Fatal(err)
	}
	defer func() {
		if err := c.Disconnect(); err != nil {
			t.Error(err)
		}
	}()
	info, err := c.Exec(ctx, "sc -d OfflineFixture")
	if err != nil {
		t.Fatal(err)
	}
	classes := jvmpkg.SearchClassInfos(info)
	if len(classes) != 1 {
		t.Fatal("fixture loader missing")
	}
	jad := c.JadParameters(ctx, jvmpkg.JavaMethod{ClassName: "OfflineFixture", MethodName: "describe", Loader: classes[0].Hash})
	value := jadTestResult(`["world",2]`, `[{"name":null,"type":"java.lang.String"},{"name":null,"type":"int"}]`)
	record, _ := jvmpkg.NewArthasRecord(value, jvmpkg.JavaMethod{}, "", "")
	jad.Supplement(&record, value)
	if record.Params != `{"personName":"world","repeatCount":2}` || !record.DecompiledParams || record.UnnamedParams {
		t.Fatalf("real jad did not recover debug variable names: %+v; methods: %+v", record, jad.Methods)
	}
	method := jvmpkg.JavaMethod{ClassName: "OfflineFixture", MethodName: "describe", Loader: classes[0].Hash}
	command, err := jvmpkg.ObservationCommand("watch", method)
	if err != nil {
		t.Fatal(err)
	}
	obsCtx, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	ready := make(chan struct{}, 1)
	records := make(chan jvmpkg.ArthasRecord, 2)
	finished := make(chan error, 1)
	go func() {
		build := c.WatchRecordBuilder(obsCtx, method, "", "")
		finished <- c.Observe(obsCtx, command, func(result map[string]json.RawMessage) {
			if jvmpkg.RawString(result, "type") == "enhancer" {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			if record, ok := build(result); ok {
				select {
				case records <- record:
				case <-obsCtx.Done():
				}
			}
		})
	}()
	select {
	case <-ready:
	case err := <-finished:
		t.Fatalf("watch startup failed: %v", err)
	case <-obsCtx.Done():
		t.Fatal("watch startup timed out")
	}
	if _, err := c.Exec(ctx, "ognl -c "+method.Loader+` '@OfflineFixture@describe("world",2)'`); err != nil {
		t.Fatal(err)
	}
	select {
	case record := <-records:
		if record.Params != `{"personName":"world","repeatCount":2}` || !record.DecompiledParams || record.UnnamedParams {
			t.Fatalf("watch did not apply jad fallback: %+v", record)
		}
	case err := <-finished:
		t.Fatalf("watch ended: %v", err)
	case <-obsCtx.Done():
		t.Fatal("watch result timed out")
	}
	stop()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("watch cleanup timed out")
	}
	reply, err := c.Exec(ctx, "memory")
	if err != nil || len(reply.Body.Results) == 0 {
		t.Fatalf("memory query: %v %+v", err, reply)
	}
}

func TestArthasRealJVMIntegration(t *testing.T) {
	if os.Getenv("WEBHOLA_ARTHAS_INTEGRATION") != "1" {
		t.Skip("requires JDK, Arthas 4.3.5+ and WEBHOLA_SPRING_CLASSPATH")
	}
	for _, retainNames := range []bool{true, false} {
		t.Run(fmt.Sprintf("retain_parameter_names_%v", retainNames), func(t *testing.T) {
			testArthasRealJVM(t, retainNames)
		})
	}
}

func testArthasRealJVM(t *testing.T, retainNames bool) {
	cfg := jvmpkg.ArthasConfig{JavaPath: os.Getenv("WEBHOLA_JAVA"), BootPath: os.Getenv("WEBHOLA_ARTHAS_BOOT")}
	java, err := cfg.Java()
	if err != nil {
		t.Fatal(err)
	}
	cp := os.Getenv("WEBHOLA_SPRING_CLASSPATH")
	if cp == "" {
		t.Fatal("set WEBHOLA_SPRING_CLASSPATH to Spring MVC and servlet dependency JARs")
	}
	dir := t.TempDir()
	javac := filepath.Join(filepath.Dir(java), "javac.exe")
	if _, err := os.Stat(javac); err != nil {
		javac = filepath.Join(filepath.Dir(java), "javac")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	compileArgs := []string{"-cp", cp, "-d", dir, filepath.Join("testdata", "ArthasFixture.java")}
	if retainNames {
		compileArgs = append([]string{"-parameters"}, compileArgs...)
	}
	compile := exec.CommandContext(ctx, javac, compileArgs...)
	platformpkg.HideProcess(compile)
	if b, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("javac: %v %s", err, b)
	}
	cmd := exec.CommandContext(ctx, java, "-cp", dir+string(os.PathListSeparator)+cp, "ArthasFixture")
	platformpkg.HideProcess(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(line, err)
	}
	listeners, err := discoverypkg.ListListeners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolveJVMTarget(listeners, port, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	// Reserve an unused port, then release it immediately before attach.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.HTTPPort = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	c, err := jvmpkg.AttachArthas(ctx, cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Disconnect(); err != nil {
			t.Error("disconnect:", err)
		}
	}()
	routes, note, err := c.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var route jvmpkg.JavaRoute
	for _, r := range routes {
		if strings.Contains(r.Mapping, "/hello/{name}") {
			route = r
		}
	}
	if route.ClassName == "" {
		debug, _ := c.Exec(ctx, "sc -d org.springframework.web.servlet.mvc.method.annotation.RequestMappingHandlerMapping")
		b, _ := json.Marshal(debug)
		t.Log(string(b))
		for _, cl := range jvmpkg.SearchClassInfos(debug) {
			dbg, e := c.Exec(ctx, fmt.Sprintf(`vmtool --action getInstances --className %s -c %s --limit 256 --express "%s" -x 5`, cl.Name, cl.Hash, jvmpkg.RouteExpression))
			b, _ := json.Marshal(dbg)
			t.Log(string(b), e)
		}
		t.Fatalf("routes not found: %+v; %s", routes, note)
	}
	t.Logf("route: %+v", route)
	requestURL := fmt.Sprintf("http://127.0.0.1:%d/hello/world", port)
	serviceInfo, err := c.Exec(ctx, "sc -d ArthasFixture$Service")
	if err != nil {
		t.Fatal(err)
	}
	infos := jvmpkg.SearchClassInfos(serviceInfo)
	if len(infos) != 1 {
		t.Fatal("service ClassLoader not found")
	}
	for _, observation := range []struct {
		kind   string
		method jvmpkg.JavaMethod
	}{
		{"watch", route.JavaMethod}, {"trace", route.JavaMethod},
		{"watch", jvmpkg.JavaMethod{ClassName: "ArthasFixture$Service", MethodName: "greet", Loader: infos[0].Hash}},
	} {
		kind := observation.kind
		command, err := jvmpkg.ObservationCommand(kind, observation.method)
		if err != nil {
			t.Fatal(err)
		}
		obsCtx, stop := context.WithTimeout(ctx, 12*time.Second)
		ready := make(chan struct{}, 1)
		result := make(chan map[string]json.RawMessage, 8)
		done := make(chan error, 1)
		go func() {
			done <- c.Observe(obsCtx, command, func(r map[string]json.RawMessage) {
				if jvmpkg.RawString(r, "type") == "enhancer" {
					select {
					case ready <- struct{}{}:
					default:
					}
				}
				if jvmpkg.RawString(r, "type") == kind {
					result <- r
				}
			})
		}()
		select {
		case <-ready:
		case err := <-done:
			stop()
			t.Fatal("observation startup:", err)
		case <-obsCtx.Done():
			stop()
			t.Fatal("no enhancer ready")
		}
		res, err := http.Get(requestURL)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		select {
		case r := <-result:
			record, ok := jvmpkg.NewArthasRecord(r, observation.method, jvmpkg.RouteKey(route), jvmpkg.RouteLabel(route))
			if !ok || (kind == "watch" && !record.Structured) {
				stop()
				t.Fatalf("invocation fields not decoded: %+v; %s", record, r["value"])
			}
			if kind == "watch" && !strings.Contains(string(r["value"]), "hello world") {
				stop()
				t.Fatalf("watch: %s", r["value"])
			}
			parameterName := "name"
			if !retainNames {
				parameterName = "arg0"
			}
			if observation.method.ClassName == route.ClassName {
				parameterName = "userName"
			}
			if kind == "watch" && record.Params != fmt.Sprintf(`{"%s":"world"}`, parameterName) {
				stop()
				t.Fatalf("parameter name not matched to value: %s; %s", record.Params, r["value"])
			}
			if kind == "watch" && record.UnnamedParams != (parameterName == "arg0") {
				stop()
				t.Fatalf("missing-name hint incorrect: %+v", record)
			}
			if kind == "trace" {
				nodes := jvmpkg.TraceMethods(r["root"])
				found := false
				for _, n := range nodes {
					if n.MethodName == "greet" {
						found = true
					}
				}
				if !found {
					stop()
					t.Fatalf("trace missing service: %s", r["root"])
				}
			}
		case err := <-done:
			stop()
			t.Fatal("observation ended before request:", err)
		case <-obsCtx.Done():
			stop()
			t.Fatal("no observation result")
		}
		stop()
		<-done
	}
}
