package desktop

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func (a *app) refreshJVMMemory(s *arthasState) {
	if s.cancel == nil || s.ctx.Err() != nil || s.target.PID == 0 || s.memoryBusy || time.Now().Before(s.memoryNext) {
		return
	}
	s.memoryBusy = true
	s.memoryNext = time.Now().Add(5 * time.Second)
	cfg, pid, birth := a.arthasConfig, s.target.PID, s.processBirth
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(s.ctx, 4*time.Second)
		defer cancel()
		m := jvmpkg.SampleJVMMemory(ctx, cfg, pid, birth)
		a.updateArthas(s, func() {
			s.memoryBusy = false
			if s.processBirth == 0 {
				s.processBirth = m.Process.Birth
			}
			s.memory = m
			if (m.Process.State == "exited" || m.Process.State == "replaced") && s.opCancel != nil {
				s.opCancel()
			}
		})
	}()
}

func (a *app) jvmMemoryValue(used, committed, limit float64) string {
	if committed <= 0 {
		return a.tr("jvm_memory.unknown")
	}
	maxText := a.tr("jvm_memory.no_limit_data")
	if limit > 0 && limit < 1<<60 {
		maxText = jvmpkg.MemoryMiB(limit)
	}
	return a.tr(i18npkg.MessageText("jvm_memory.pool", jvmpkg.MemoryMiB(used), jvmpkg.MemoryMiB(committed), maxText))
}

func (a *app) jvmProcessView(c *ui.Context, s *arthasState) {
	if s.target.PID == 0 {
		return
	}
	a.refreshJVMMemory(s)
	c.After(time.Second)
	m := s.memory
	state := m.Process.State
	if state == "" {
		state = "checking"
	}
	color := c.Theme().TextMuted
	if state == "running" {
		color = c.Theme().Accent
	} else if state == "exited" || state == "replaced" {
		color = c.Theme().Danger
	}
	ui.Column(c).FillWidth().Padding(8, 10).Gap(4).Radius(6).Border(1, c.Theme().Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, a.tr(i18npkg.MessageText("jvm_memory.target", s.target.Process, s.target.PID, s.target.Port))).Grow(1).FontSize(12).Bold().Selectable().Tooltip(m.Process.Path)
			ui.Text(c, a.tr("jvm_memory.state_"+state)).FontSize(11).TextColor(color)
		})
		if state == "exited" || state == "replaced" {
			ui.Text(c, a.tr("jvm_memory.exited_hint")).FontSize(12).TextColor(c.Theme().Danger)
			return
		}
		processText := a.tr("jvm_memory.unknown")
		if m.Process.Resident > 0 {
			processText = a.tr(i18npkg.MessageText("jvm_memory.process_usage", jvmpkg.MemoryMiB(float64(m.Process.Resident)), jvmpkg.MemoryMiB(float64(m.Process.Private))))
		}
		ui.Text(c, processText).FontSize(11).Selectable()
		ui.Text(c, a.tr(i18npkg.MessageText("jvm_memory.jvm_usage", a.jvmMemoryValue(m.HeapUsed, m.HeapCommitted, m.HeapMax), a.jvmMemoryValue(m.MetaUsed, m.MetaCommitted, m.MetaMax)))).FontSize(11).Selectable()
		if !m.Sampled.IsZero() {
			ui.Text(c, a.tr(i18npkg.MessageText("jvm_memory.updated", m.Sampled.Local().Format("15:04:05")))).FontSize(10).TextColor(c.Theme().TextMuted)
		}
		if m.Note != "" {
			ui.Text(c, a.tr(m.Note)).FontSize(11).TextColor(c.Theme().TextMuted)
		}
	})
}
