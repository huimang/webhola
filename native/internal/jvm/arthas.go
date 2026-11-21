package jvm

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
	platformpkg "webhola/native/internal/platform"
)

type ArthasConfig struct {
	JavaPath string `json:"javaPath,omitempty"`
	BootPath string `json:"arthasBootPath,omitempty"`
	HTTPPort string `json:"arthasHttpPort,omitempty"`
}

func (s ArthasConfig) Port() (int, error) {
	p := strings.TrimSpace(s.HTTPPort)
	if p == "" {
		return 8563, nil
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, i18npkg.MessageError("arthas.arthas_http_port_must_be_between_1")
	}
	return n, nil
}

func dependencyFile(path, label string) (string, error) {
	path = platformpkg.CleanTSharkPath(path)
	if !filepath.IsAbs(path) {
		return "", i18npkg.MessageError("arthas.select_the_full_path_to_s", label)
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", i18npkg.MessageError("arthas.s_is_unavailable_w", label, err)
	}
	if st.IsDir() {
		return "", i18npkg.MessageError("arthas.select_a_s_file", label)
	}
	return path, nil
}

func (s ArthasConfig) Java() (string, error) {
	p := platformpkg.CleanTSharkPath(s.JavaPath)
	if p == "" {
		p = os.Getenv("WEBHOLA_JAVA")
	}
	if p != "" {
		return dependencyFile(p, "Java")
	}
	if home := os.Getenv("JAVA_HOME"); home != "" {
		for _, name := range []string{"java.exe", "java"} {
			if p, err := dependencyFile(filepath.Join(home, "bin", name), "Java"); err == nil {
				return p, nil
			}
		}
	}
	p, err := exec.LookPath("java")
	if err != nil {
		return "", i18npkg.MessageError("arthas.java_not_found_select_a_compatible_jdk")
	}
	return p, nil
}

func (s ArthasConfig) Boot() (string, error) {
	p := platformpkg.CleanTSharkPath(s.BootPath)
	if p == "" {
		p = os.Getenv("WEBHOLA_ARTHAS_BOOT")
	}
	if p == "" {
		return "", i18npkg.MessageError("arthas.select_arthas_boot_jar_in_dependency_settings")
	}
	p, err := dependencyFile(p, "arthas-boot.jar")
	if err != nil {
		return "", err
	}
	r, err := zip.OpenReader(p)
	if err != nil {
		return "", i18npkg.MessageError("arthas.invalid_arthas_jar_w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name == "com/taobao/arthas/boot/Bootstrap.class" {
			return p, nil
		}
	}
	return "", i18npkg.MessageError("arthas.the_selected_jar_is_not_arthas_bootstrap")
}

type ArthasReply struct {
	State      string `json:"state"`
	Message    string `json:"message"`
	SessionID  string `json:"sessionId"`
	ConsumerID string `json:"consumerId"`
	Body       struct {
		JobID       int                          `json:"jobId"`
		JobStatus   string                       `json:"jobStatus"`
		TimeExpired bool                         `json:"timeExpired"`
		Results     []map[string]json.RawMessage `json:"results"`
	} `json:"body"`
}

type ArthasClient struct {
	jadMu    sync.Mutex
	jadCache map[string]jadParameters
	Endpoint string
	Pid      uint32
	owned    bool
	Http     *http.Client
}

func newArthasClient(port int, pid uint32) *ArthasClient {
	return &ArthasClient{Endpoint: fmt.Sprintf("http://127.0.0.1:%d/api", port), Pid: pid,
		Http: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}, Timeout: 40 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func RawString(m map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}

func (c *ArthasClient) call(ctx context.Context, payload map[string]any) (ArthasReply, error) {
	var out ArthasReply
	b, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Http.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	const limit = 8 << 20
	b, err = io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return out, err
	}
	if len(b) > limit {
		return out, i18npkg.MessageError("arthas.arthas_output_exceeds_8_mib_narrow_the")
	}
	if res.StatusCode != http.StatusOK {
		return out, fmt.Errorf("Arthas HTTP %d", res.StatusCode)
	}
	if err = json.Unmarshal(b, &out); err != nil {
		return out, i18npkg.MessageError("arthas.arthas_returned_invalid_results_w", err)
	}
	if out.State != "SUCCEEDED" && out.State != "SCHEDULED" {
		return out, fmt.Errorf("Arthas %s：%s", out.State, out.Message)
	}
	for _, r := range out.Body.Results {
		if RawString(r, "type") == "status" {
			var code int
			_ = json.Unmarshal(r["statusCode"], &code)
			if code != 0 {
				return out, i18npkg.MessageError("arthas.arthas_command_failed_d_s", code, RawString(r, "message"))
			}
		}
	}
	return out, nil
}

func (c *ArthasClient) Exec(ctx context.Context, command string) (ArthasReply, error) {
	r, err := c.call(ctx, map[string]any{"action": "exec", "command": command, "execTimeout": 10000})
	if err == nil && r.Body.TimeExpired {
		err = i18npkg.MessageError("arthas.arthas_command_timed_out_scan_results_may")
	}
	return r, err
}

func (c *ArthasClient) cleanupSession(session string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, _ = c.call(ctx, map[string]any{"action": "interrupt_job", "sessionId": session})
	_, _ = c.call(ctx, map[string]any{"action": "close_session", "sessionId": session})
}

// Validate the welcome PID before executing diagnostic commands. A port alone is
// not sufficient: another JVM may already own the configured Arthas endpoint.
func (c *ArthasClient) Verify(ctx context.Context) error {
	s, err := c.call(ctx, map[string]any{"action": "init_session"})
	if err != nil {
		return err
	}
	if s.SessionID == "" || s.ConsumerID == "" {
		return i18npkg.MessageError("arthas.arthas_did_not_return_a_session")
	}
	defer c.cleanupSession(s.SessionID)
	r, err := c.call(ctx, map[string]any{"action": "pull_results", "sessionId": s.SessionID, "consumerId": s.ConsumerID})
	if err != nil {
		return err
	}
	for _, result := range r.Body.Results {
		if RawString(result, "type") == "welcome" {
			pid := strings.Trim(string(result["pid"]), "\"")
			if pid == strconv.FormatUint(uint64(c.Pid), 10) {
				return nil
			}
			return i18npkg.MessageError("arthas.the_arthas_port_belongs_to_pid_s", pid, c.Pid)
		}
	}
	return i18npkg.MessageError("arthas.unable_to_verify_the_arthas_target_pid")
}

func AttachArthas(ctx context.Context, cfg ArthasConfig, target discoverypkg.Listener) (*ArthasClient, error) {
	if !target.JVM || target.PID == 0 {
		return nil, i18npkg.MessageError("arthas.the_target_port_is_not_confirmed_as")
	}
	port, err := cfg.Port()
	if err != nil {
		return nil, err
	}
	c := newArthasClient(port, target.PID)
	defer func() { c.Http.CloseIdleConnections() }()
	conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if dialErr == nil {
		conn.Close()
		if err := c.Verify(ctx); err != nil {
			return nil, err
		}
		return c, nil
	}
	java, err := cfg.Java()
	if err != nil {
		return nil, err
	}
	boot, err := cfg.Boot()
	if err != nil {
		return nil, err
	}
	if err := checkArthasMetaspace(ctx, java, target.PID); err != nil {
		return nil, err
	}
	// Zero selects an ephemeral loopback telnet port and avoids Bootstrap
	// 4.3.5's negative-port preflight bug. Diagnostic traffic uses HTTP only.
	args := []string{"-jar", boot, "--target-ip", "127.0.0.1", "--telnet-port", "0", "--http-port", strconv.Itoa(port), "--attach-only"}
	// A full distribution runs offline; a standalone boot jar retains its
	// normal download behavior for existing user configurations.
	home := filepath.Dir(boot)
	full := true
	for _, jar := range []string{"arthas-core.jar", "arthas-agent.jar", "arthas-spy.jar"} {
		if info, err := os.Stat(filepath.Join(home, jar)); err != nil || !info.Mode().IsRegular() {
			full = false
			break
		}
	}
	if full {
		args = append(args, "--arthas-home", home)
	}
	args = append(args, strconv.FormatUint(uint64(target.PID), 10))
	cmd := exec.CommandContext(ctx, java, args...)
	cmd.Dir = home
	platformpkg.HideProcess(cmd)
	var out platformpkg.TailWriter
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			c.cleanupFailedAttach()
		}
		return nil, i18npkg.MessageError("arthas.arthas_attach_failed_w_s", err, string(out.Buf))
	}
	if err := c.waitReady(ctx, 5*time.Second); err != nil {
		c.cleanupFailedAttach()
		return nil, arthasAttachFailure(ctx, java, target.PID, err, string(out.Buf))
	}
	c.owned = true
	return c, nil
}

func (c *ArthasClient) cleanupFailedAttach() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c.Verify(ctx) == nil {
		c.owned = true
		_ = c.Disconnect()
	}
}

func (c *ArthasClient) Disconnect() error {
	defer c.Http.CloseIdleConnections()
	if !c.owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := c.Verify(ctx); err != nil {
		return err
	}
	_, err := c.Exec(ctx, "stop")
	// stop may tear down Netty before the HTTP reply has been sent. Confirm
	// endpoint closure before treating EOF / connection reset as a failure.
	for i := 0; i < 20; i++ {
		conn, dialErr := net.DialTimeout("tcp", strings.TrimSuffix(strings.TrimPrefix(c.Endpoint, "http://"), "/api"), 100*time.Millisecond)
		if dialErr != nil {
			return nil
		}
		conn.Close()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		return err
	}
	return i18npkg.MessageError("arthas.the_arthas_port_is_still_listening_after")
}

type JavaMethod struct {
	ClassName  string `json:"className"`
	MethodName string `json:"methodName"`
	Loader     string `json:"loader"`
}

type javaClassInfo struct {
	Name string `json:"name"`
	Hash string `json:"classLoaderHash"`
}

func SearchClassInfos(r ArthasReply) []javaClassInfo {
	var out []javaClassInfo
	for _, result := range r.Body.Results {
		if RawString(result, "type") != "sc" {
			continue
		}
		var info javaClassInfo
		if json.Unmarshal(result["classInfo"], &info) == nil && info.Name != "" {
			out = append(out, info)
		}
	}
	return out
}

type JavaRoute struct {
	JavaMethod
	Mapping   string `json:"mapping"`
	Signature string `json:"signature"`
}

var JavaClassName = regexp.MustCompile(`^[\pL_$][\pL\pN_.$]*$`)

var JavaMethodName = regexp.MustCompile(`^[\pL_$][\pL\pN_$]*$`)

var LoaderHash = regexp.MustCompile(`^[0-9a-fA-F]{1,8}$`)

func ObservationCommand(kind string, m JavaMethod) (string, error) {
	if !JavaClassName.MatchString(m.ClassName) || !JavaMethodName.MatchString(m.MethodName) || !LoaderHash.MatchString(m.Loader) {
		return "", i18npkg.MessageError("arthas.select_a_valid_class_method_and_classloader")
	}
	// Regex anchors prevent accidental wildcard matches in nested class names.
	base := fmt.Sprintf("%s -E '%s' '%s' --classloader %s -n 100000", kind, "^"+regexp.QuoteMeta(m.ClassName)+"$", "^"+regexp.QuoteMeta(m.MethodName)+"$", m.Loader)
	switch kind {
	case "watch":
		// Use the agent's serializer through Advice's own loader. This avoids
		// depending on an application JSON library or changing global Arthas
		// options shared with other clients. ObjectView handles cycles and skips
		// non-field getters. The HTTP layer wraps the JSON string in ObjectVO.
		return base + ` "` + watchExpression() + `" -x 1 -M 262144 -f`, nil
	case "trace":
		return base + " --skipJDKMethod true", nil
	default:
		return "", i18npkg.MessageError("arthas.unsupported_observation_mode")
	}
}

func watchExpression() string {
	// Resolve the exact invoked overload from Advice's JVM descriptor. Parameter
	// values must never be paired with names from another overload.
	return `#types=@java.lang.invoke.MethodType@fromMethodDescriptorString(method.descriptor,loader),#m=clazz.getDeclaredMethods().{? (#candidate=#this,#candidate.getName().equals(#root.method.name) && #candidate.getReturnType().equals(#types.returnType()) && @java.util.Arrays@equals(#candidate.getParameterTypes(),#types.parameterArray()))}[0],` +
		`#parameters=#m.getParameters().{(#p=#this,#annotations=#p.getAnnotations().{? #this.annotationType().getName().matches('org.springframework.web.bind.annotation.(RequestParam|PathVariable|RequestHeader|CookieValue|RequestPart|MatrixVariable|RequestAttribute|SessionAttribute|ModelAttribute)')},#{'name':(#annotations.size()>0 && #annotations[0].value().length()>0?#annotations[0].value():(#annotations.size()>0 && #annotations[0].name().length()>0?#annotations[0].name():(#p.isNamePresent()?#p.getName():null))),'type':#p.getType().getTypeName()})},` +
		`#data={params,returnObj,throwExp,#parameters},` +
		`#serializer=@java.lang.Class@forName('com.taobao.arthas.core.view.ObjectView',true,#root.getClass().getClassLoader()).getMethods().{? #this.getName().equals('toJsonString') && #this.getParameterTypes().length==1}[0],#serializer.invoke(null,#data)`
}

// Read the live registry rather than guessing routes from controller annotation
// names. This includes composed annotations, inherited and programmatic mappings.
var RouteExpression = routeScanExpression()

func routeScanExpression() string {
	// Recent Arthas HTTP versions render ObjectVO as display text rather than
	// JSON. Emit a versioned string of base64 fields using only JDK classes;
	// route punctuation and Unicode cannot collide with record delimiters.
	fields := []string{"#e.getKey().toString()", "#m.getDeclaringClass().getName()", "#m.getName()", "#m.toGenericString()", "(#m.getDeclaringClass().getClassLoader()==null?'0':@java.lang.Integer@toHexString(#m.getDeclaringClass().getClassLoader().hashCode()))"}
	var b strings.Builder
	b.WriteString("#b=new java.lang.StringBuilder(),#b.append('WEBHOLA1;').append(instances.length).append(';'),instances.{#this.getHandlerMethods().entrySet().{(#e=#this,#m=#e.getValue().getMethod(),")
	for i, f := range fields {
		if i > 0 {
			b.WriteString("#b.append('|'),")
		}
		b.WriteString("#b.append(@java.util.Base64@getEncoder().encodeToString((" + f + ").getBytes('UTF-8'))),")
	}
	b.WriteString("#b.append(';'))}},#b.toString()")
	return b.String()
}

func DecodeRouteValue(raw json.RawMessage) ([]JavaRoute, int, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, 0, err
	}
	if s, ok := value.(string); ok {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "@String[") && strings.HasSuffix(s, "]") {
			s = strings.TrimSuffix(strings.TrimPrefix(s, "@String["), "]")
		}
		parts := strings.Split(s, ";")
		if len(parts) < 3 || parts[0] != "WEBHOLA1" || parts[len(parts)-1] != "" {
			return nil, 0, i18npkg.MessageError("arthas.the_arthas_route_response_is_incompatible_or")
		}
		count, err := strconv.Atoi(parts[1])
		if err != nil || count < 0 {
			return nil, 0, i18npkg.MessageError("arthas.invalid_route_instance_count")
		}
		var routes []JavaRoute
		for _, row := range parts[2 : len(parts)-1] {
			fields := strings.Split(row, "|")
			if len(fields) != 5 {
				return nil, count, i18npkg.MessageError("arthas.incomplete_route_fields")
			}
			for i, f := range fields {
				b, err := base64.StdEncoding.DecodeString(f)
				if err != nil {
					return nil, count, err
				}
				fields[i] = string(b)
			}
			routes = append(routes, JavaRoute{JavaMethod: JavaMethod{ClassName: fields[1], MethodName: fields[2], Loader: fields[4]}, Mapping: fields[0], Signature: fields[3]})
		}
		return routes, count, nil
	}
	var routes []JavaRoute
	CollectRoutes(value, &routes)
	return routes, 0, nil
}

func CollectRoutes(v any, routes *[]JavaRoute) {
	switch x := v.(type) {
	case []any:
		for _, child := range x {
			CollectRoutes(child, routes)
		}
	case map[string]any:
		if nested, ok := x["routes"]; ok {
			CollectRoutes(nested, routes)
		}
		if _, ok := x["mapping"].(string); ok {
			b, _ := json.Marshal(x)
			var r JavaRoute
			if json.Unmarshal(b, &r) == nil && r.ClassName != "" && r.MethodName != "" {
				*routes = append(*routes, r)
			}
		}
	}
}

func (c *ArthasClient) Routes(ctx context.Context) ([]JavaRoute, string, error) {
	var routes []JavaRoute
	var notes []string
	for _, class := range []string{"org.springframework.web.servlet.mvc.method.annotation.RequestMappingHandlerMapping", "org.springframework.web.reactive.result.method.annotation.RequestMappingHandlerMapping"} {
		sc, err := c.Exec(ctx, "sc -d "+class)
		if err != nil {
			return routes, strings.Join(notes, "\n"), err
		}
		loaders := map[string]bool{}
		for _, cl := range SearchClassInfos(sc) {
			if cl.Name == class && LoaderHash.MatchString(cl.Hash) {
				loaders[cl.Hash] = true
			}
		}
		for loader := range loaders {
			cmd := fmt.Sprintf(`vmtool --action getInstances --className %s -c %s --limit 256 --express "%s" -x 5`, class, loader, RouteExpression)
			r, err := c.Exec(ctx, cmd)
			if err != nil {
				notes = append(notes, class+": "+i18npkg.ErrorMessage(err))
				continue
			}
			for _, result := range r.Body.Results {
				if RawString(result, "type") == "vmtool" {
					found, count, err := DecodeRouteValue(result["value"])
					if err != nil {
						notes = append(notes, i18npkg.ErrorMessage(err))
						continue
					}
					routes = append(routes, found...)
					if count >= 256 {
						notes = append(notes, i18npkg.MessageText("arthas.reached_the_limit_of_256_handlermapping_instances"))
					}
				}
			}
		}
	}
	unique := map[string]JavaRoute{}
	for _, r := range routes {
		unique[r.Mapping+r.Signature+r.Loader] = r
	}
	routes = nil
	for _, r := range unique {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Mapping+routes[i].Signature+routes[i].Loader < routes[j].Mapping+routes[j].Signature+routes[j].Loader
	})
	return routes, strings.Join(notes, "\n"), nil
}

// Each observation owns a separate session, so cancellation never interrupts
// another client. Cleanup also runs after transport errors or application exit.
func (c *ArthasClient) Observe(ctx context.Context, command string, emit func(map[string]json.RawMessage)) error {
	s, err := c.call(ctx, map[string]any{"action": "init_session"})
	if err != nil {
		return err
	}
	if s.SessionID == "" || s.ConsumerID == "" {
		return i18npkg.MessageError("arthas.arthas_did_not_create_an_observation_session")
	}
	defer c.cleanupSession(s.SessionID)
	r, err := c.call(ctx, map[string]any{"action": "async_exec", "sessionId": s.SessionID, "command": command})
	if err != nil {
		return err
	}
	job := r.Body.JobID
	if job <= 0 {
		return i18npkg.MessageError("arthas.arthas_did_not_return_an_observation_task")
	}
	for ctx.Err() == nil {
		r, err = c.call(ctx, map[string]any{"action": "pull_results", "sessionId": s.SessionID, "consumerId": s.ConsumerID})
		if err != nil {
			return err
		}
		for _, result := range r.Body.Results {
			var id int
			_ = json.Unmarshal(result["jobId"], &id)
			if id != job {
				continue
			}
			emit(result)
			if RawString(result, "type") == "status" {
				return nil
			}
		}
	}
	return ctx.Err()
}
