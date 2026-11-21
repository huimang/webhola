package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

type arthasState struct {
	memory                      jvmpkg.JVMMemorySnapshot
	memoryBusy                  bool
	memoryNext                  time.Time
	processBirth                uint64
	ctx                         context.Context
	cancel                      context.CancelFunc
	jobs                        sync.WaitGroup
	mu                          sync.Mutex
	client                      *jvmpkg.ArthasClient
	target                      discoverypkg.Listener
	busy                        bool
	observing                   bool
	pendingDrill                *jvmpkg.JavaMethod
	opCancel                    context.CancelFunc
	loading                     *loadingDialog
	status, search, routeChoice string
	routes                      []jvmpkg.JavaRoute
	method                      jvmpkg.JavaMethod
	loaderChoices               []string
	records                     []jvmpkg.ArthasRecord
	nextRecord, selectedRecord  uint64
	detailTab                   string
	preview                     *bodyPreview
	historyBytes                int
}

func (s *arthasState) connection() *jvmpkg.ArthasClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *arthasState) setConnection(c *jvmpkg.ArthasClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = c
}

func (a *app) updateArthas(s *arthasState, fn func()) {
	if s.ctx.Err() != nil {
		return
	}
	a.update(func() {
		if s.ctx.Err() == nil {
			fn()
		}
	})
}

func resolveJVMTarget(list []discoverypkg.Listener, port int, pid uint32) (discoverypkg.Listener, error) {
	var result discoverypkg.Listener
	for _, l := range list {
		if l.Port != port || (pid != 0 && l.PID != pid) {
			continue
		}
		if result.PID != 0 && result.PID != l.PID {
			return discoverypkg.Listener{}, i18npkg.MessageError("arthas_ui.multiple_processes_use_this_port_select_a")
		}
		result = l
	}
	if result.PID == 0 {
		return result, i18npkg.MessageError("arthas_ui.no_local_listening_process_found_for_this")
	}
	if !result.JVM {
		return result, i18npkg.MessageError("arthas_ui.this_port_was_not_identified_as_a")
	}
	if result.PID == uint32(os.Getpid()) {
		return result, i18npkg.MessageError("arthas_ui.cannot_connect_webhola_to_itself")
	}
	return result, nil
}

func (a *app) closeArthas() {
	s := a.arthas
	if s == nil {
		return
	}
	s.loading.close()
	if a.bodyFullscreen != nil && s.preview != nil && a.bodyFullscreen.key == s.preview.key {
		a.closeBodyFullscreen()
	}
	s.cancel()
	a.arthas = nil
	a.workspace = 0
}

func (a *app) openArthas() {
	if a.arthas != nil {
		a.workspace = 1
		return
	}
	known, ok := a.selectedJVMService()
	if !ok || a.scanning || a.pcap != "" {
		a.scanStatus = i18npkg.MessageText("arthas_ui.scan_and_select_a_confirmed_jvm_service")
		return
	}
	if a.arthasCleanup != nil {
		select {
		case <-a.arthasCleanup:
		default:
			a.scanStatus = i18npkg.MessageText("arthas_ui.cleaning_up_the_previous_jvm_connection_reopen")
			return
		}
	}
	port, pid := known.Port, known.PID
	ctx, cancel := context.WithCancel(a.ctx)
	s := &arthasState{ctx: ctx, cancel: cancel, target: known, status: i18npkg.MessageText("arthas_ui.verifying_port_and_jvm_process")}
	s.memory.Process = jvmpkg.ReadJVMProcess(known.PID)
	s.processBirth = s.memory.Process.Birth
	a.arthas = s
	cleanupDone := make(chan struct{})
	a.arthasCleanup = cleanupDone
	a.workspace = 1
	// A lifetime worker waits for pending commands to finish their own session
	// cleanup before stopping only the agent that this tab started.
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		defer close(cleanupDone)
		<-ctx.Done()
		s.jobs.Wait()
		if c := s.connection(); c != nil {
			_ = c.Disconnect()
		}
	}()
	a.arthasJob(s, 30*time.Second, func(ctx context.Context) error {
		list, err := discoverypkg.ListListeners(ctx)
		if err != nil {
			return err
		}
		target, err := resolveJVMTarget(list, port, pid)
		if err != nil {
			return err
		}
		a.updateArthas(s, func() {
			s.target = target
			s.status = i18npkg.MessageText("arthas_ui.jvm_identified_s_port_d_pid_d", target.Process, target.Port, target.PID)
		})
		return nil
	})
}

func (a *app) arthasJob(s *arthasState, timeout time.Duration, work func(context.Context) error) {
	if s.busy || s.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	s.busy = true
	s.opCancel = cancel
	s.jobs.Add(1)
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		defer s.jobs.Done()
		defer cancel()
		err := work(ctx)
		a.updateArthas(s, func() {
			s.busy = false
			s.observing = false
			s.opCancel = nil
			s.loading.close()
			s.loading = nil
			if errors.Is(err, context.DeadlineExceeded) {
				s.status = i18npkg.MessageText("arthas_ui.observation_timed_out_and_stopped_observe_again")
			} else if errors.Is(err, context.Canceled) {
				s.status = i18npkg.MessageText("arthas_ui.operation_stopped")
			} else if err != nil {
				s.status = i18npkg.ErrorMessage(err)
			}
			if next := s.pendingDrill; next != nil {
				s.pendingDrill = nil
				// The old observation has returned and cleaned up its session.
				a.drillArthas(s, *next)
			}
		})
	}()
}

func (a *app) scanArthasRoutes(s *arthasState) {
	if s.busy || s.ctx.Err() != nil {
		return
	}
	config, target := a.arthasConfig, s.target
	s.status = i18npkg.MessageText("loading.verifying_target")
	s.routes = nil
	s.method = jvmpkg.JavaMethod{}
	title := "loading.routes_title"
	if s.connection() != nil {
		title = "loading.rescan_title"
	}
	s.loading = a.openLoading(a.win, title, "loading.routes_hint", func() string { return s.status }, func() {
		if s.opCancel != nil {
			s.opCancel()
		}
	}, 5)
	// Update only when a real phase finishes; downloads and JVM commands
	// have no reliable time-based percentage and remain at their current phase.
	dialog := s.loading
	progress := func(completed int, statusKey string) {
		a.updateArthas(s, func() {
			if s.loading != dialog || !dialog.active {
				return
			}
			dialog.completed = completed
			s.status = i18npkg.MessageText(statusKey)
			if dialog.window != nil {
				dialog.window.Invalidate()
			}
		})
	}
	a.arthasJob(s, 90*time.Second, func(ctx context.Context) error {
		c := s.connection()
		if c == nil {
			// Refresh the listener/PID association just before attaching.
			list, err := discoverypkg.ListListeners(ctx)
			if err != nil {
				return err
			}
			fresh, err := resolveJVMTarget(list, target.Port, target.PID)
			if err != nil {
				return err
			}
			progress(1, "loading.connecting_jvm")
			c, err = jvmpkg.AttachArthas(ctx, config, fresh)
			if err != nil {
				return err
			}
			s.setConnection(c)
		}
		progress(2, "loading.verifying_session")
		if err := c.Verify(ctx); err != nil {
			return err
		}
		progress(3, "loading.reading_routes")
		routes, note, err := c.Routes(ctx)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		progress(4, "loading.organizing_routes")
		a.updateArthas(s, func() {
			s.routes = routes
			s.method = jvmpkg.JavaMethod{}
			if len(routes) > 0 {
				key := s.routeChoice
				found := false
				for _, r := range routes {
					if jvmpkg.RouteKey(r) == key {
						found = true
						break
					}
				}
				if !found {
					key = jvmpkg.RouteKey(routes[0])
				}
				s.selectRoute(key)
			}
			s.status = i18npkg.MessageText("arthas_ui.connected_to_pid_d_read_d_spring", c.Pid, len(routes))
			if len(routes) == 0 && note == "" {
				s.status += i18npkg.MessageText("arthas_ui.no_mvc_webflux_handlermapping_found_other_frameworks")
			}
			if note != "" {
				s.status += i18npkg.MessageText("arthas_ui.scan_incomplete") + note
			}
			dialog.completed = 5
		})
		return nil
	})
}

func (a *app) observeArthas(s *arthasState, kind string) {
	c := s.connection()
	if c == nil || s.busy {
		return
	}
	command, err := jvmpkg.ObservationCommand(kind, s.method)
	if err != nil {
		s.status = i18npkg.ErrorMessage(err)
		return
	}
	method, routeKey, routeLabel := s.method, s.routeChoice, s.currentRouteLabel()
	s.observing = true
	s.status = kind + i18npkg.MessageText("arthas_ui.installing_observation_point_once_ready_send_up")
	a.arthasJob(s, 30*time.Minute, func(ctx context.Context) error {
		buildRecord := func(result map[string]json.RawMessage) (jvmpkg.ArthasRecord, bool) {
			return jvmpkg.NewArthasRecord(result, method, routeKey, routeLabel)
		}
		if kind == "watch" {
			buildRecord = c.WatchRecordBuilder(ctx, method, routeKey, routeLabel)
		}
		return c.Observe(ctx, command, func(result map[string]json.RawMessage) {
			record, ok := buildRecord(result)
			a.updateArthas(s, func() {
				if ok {
					s.acceptRecord(record)
				}
				if jvmpkg.RawString(result, "type") == "enhancer" {
					s.status = i18npkg.MessageText("arthas_ui.observation_point_ready_send_up_to_5")
				}
				if jvmpkg.RawString(result, "type") == "status" {
					s.status = i18npkg.MessageText("arthas_ui.observation_ended_results_have_been_retained")
				}
			})
		})
	})
}

func (a *app) disconnectArthas(s *arthasState) {
	c := s.connection()
	if c == nil || s.busy {
		return
	}
	s.status = i18npkg.MessageText("arthas_ui.disconnecting_and_cleaning_up_observations")
	a.arthasJob(s, 15*time.Second, func(context.Context) error {
		if err := c.Disconnect(); err != nil {
			return err
		}
		s.setConnection(nil)
		a.updateArthas(s, func() {
			s.status = i18npkg.MessageText("arthas_ui.disconnected_observation_results_have_been_retained")
		})
		return nil
	})
}

func (a *app) drillArthas(s *arthasState, m jvmpkg.JavaMethod) {
	c := s.connection()
	if c == nil {
		return
	}
	if !jvmpkg.JavaClassName.MatchString(m.ClassName) || !jvmpkg.JavaMethodName.MatchString(m.MethodName) {
		s.status = i18npkg.MessageText("arthas_ui.this_node_is_not_a_traceable_java")
		return
	}
	if s.busy {
		if s.observing && s.pendingDrill == nil && s.opCancel != nil {
			s.pendingDrill = &m
			s.status = i18npkg.MessageText("arthas_ui.stopping_observation_for_drill")
			s.opCancel()
		}
		return
	}
	s.status = i18npkg.MessageText("arthas_ui.checking_classloader_for_drill_down_method")
	a.arthasJob(s, 15*time.Second, func(ctx context.Context) error {
		r, err := c.Exec(ctx, "sc -d "+m.ClassName)
		if err != nil {
			return err
		}
		loaders := map[string]bool{}
		for _, cl := range jvmpkg.SearchClassInfos(r) {
			if cl.Name == m.ClassName && jvmpkg.LoaderHash.MatchString(cl.Hash) {
				loaders[cl.Hash] = true
			}
		}
		if len(loaders) == 1 {
			for hash := range loaders {
				m.Loader = hash
			}
		}
		a.updateArthas(s, func() {
			s.method = m
			s.loaderChoices = nil
			for hash := range loaders {
				s.loaderChoices = append(s.loaderChoices, hash)
			}
			sort.Strings(s.loaderChoices)
			s.status = i18npkg.MessageText("arthas_ui.drill_down_method_selected_continue_with_watch")
			if m.Loader == "" {
				s.status = i18npkg.MessageText("arthas_ui.the_classloader_is_ambiguous_enter_the_corresponding")
			}
		})
		return nil
	})
}

func routeMatches(r jvmpkg.JavaRoute, q string) bool {
	return strings.Contains(strings.ToLower(r.Mapping+" "+r.ClassName+" "+r.MethodName+" "+r.Signature), strings.ToLower(strings.TrimSpace(q)))
}
