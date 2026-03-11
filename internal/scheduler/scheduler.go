package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Handgrip/global-telemetry/internal/config"
	"github.com/Handgrip/global-telemetry/internal/probe"
	"github.com/Handgrip/global-telemetry/internal/reporter"
)

const maxBufferSize = 10000

type Scheduler struct {
	configMgr    *config.ConfigManager
	reporter     reporter.Reporter
	pushInterval time.Duration

	mu     sync.Mutex
	buffer []*probe.RawResult
}

func New(cm *config.ConfigManager, rep reporter.Reporter, pushInterval time.Duration) *Scheduler {
	return &Scheduler{
		configMgr:    cm,
		reporter:     rep,
		pushInterval: pushInterval,
	}
}

// Run starts per-target probe goroutines, a push loop, and a config refresh loop.
// It restarts probe goroutines when the config changes. Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	go s.configMgr.StartRefreshLoop(ctx)

	pushTicker := time.NewTicker(s.pushInterval)
	defer pushTicker.Stop()

	var (
		probeCancel context.CancelFunc
		probeWg     sync.WaitGroup
	)

	startProbes := func() {
		// Cancel any existing probe context before starting new ones
		if probeCancel != nil {
			probeCancel()
			probeWg.Wait()
			probeCancel = nil
		}
		
		targets := s.configMgr.GetTargets()
		if targets == nil || len(targets.Targets) == 0 {
			slog.Warn("no targets configured")
			return
		}

		var probeCtx context.Context
		probeCtx, probeCancel = context.WithCancel(ctx)
		defaults := targets.Defaults

		for _, t := range targets.Targets {
			t := t
			probeWg.Add(1)
			go func() {
				defer probeWg.Done()
				s.probeLoop(probeCtx, t, defaults)
			}()
		}

		slog.Info("scheduler started",
			"push_interval", s.pushInterval,
			"targets", len(targets.Targets),
		)
	}

	stopProbes := func() {
		if probeCancel != nil {
			probeCancel()
			probeWg.Wait()
			probeCancel = nil
		}
	}

	startProbes()

	for {
		select {
		case <-ctx.Done():
			stopProbes()
			slog.Info("scheduler shutting down, flushing buffer")
			flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.flush(flushCtx)
			flushCancel()
			return
		case <-s.configMgr.Changed():
			slog.Info("config changed, restarting probe goroutines")
			stopProbes()
			startProbes()
		case <-pushTicker.C:
			s.flush(ctx)
		}
	}
}

// probeLoop runs a single target's probe on its own interval.
// A random initial delay in [0, interval) spreads probes out to avoid thundering herd.
func (s *Scheduler) probeLoop(ctx context.Context, target config.Target, defaults config.TargetDefaults) {
	interval := target.GetInterval(defaults)
	timeout := target.GetTimeout(defaults)

	p := probe.ForType(target.Type)
	if p == nil {
		slog.Warn("unknown probe type", "type", target.Type, "target", target.Name)
		return
	}

	jitter := time.Duration(rand.Int64N(int64(interval)))
	slog.Debug("probe loop started", "target", target.Name, "interval", interval, "timeout", timeout, "initial_delay", jitter)

	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	s.probeTarget(ctx, p, target, timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.probeTarget(ctx, p, target, timeout)
			ticker.Reset(interval)
		}
	}
}

func (s *Scheduler) probeTarget(ctx context.Context, p probe.Probe, target config.Target, timeout time.Duration) {
	result, err := p.Run(ctx, target, timeout)
	if err != nil {
		slog.Error("probe execution error", "target", target.Name, "error", err)
		return
	}

	s.mu.Lock()
	s.buffer = append(s.buffer, result)
	if len(s.buffer) > maxBufferSize {
		dropped := len(s.buffer) - maxBufferSize
		s.buffer = s.buffer[dropped:]
		slog.Warn("buffer overflow, dropped oldest samples", "dropped", dropped)
	}
	s.mu.Unlock()
}

func (s *Scheduler) flush(ctx context.Context) {
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return
	}
	toSend := make([]*probe.RawResult, len(s.buffer))
	copy(toSend, s.buffer)
	s.buffer = s.buffer[:0]
	s.mu.Unlock()

	if err := s.reporter.Report(ctx, toSend); err != nil {
		slog.Error("push failed, re-buffering samples", "error", err, "samples", len(toSend))
		s.mu.Lock()
		// Re-prepend failed samples to the front while preserving order
		newBuffer := make([]*probe.RawResult, 0, len(toSend)+len(s.buffer))
		newBuffer = append(newBuffer, toSend...)
		newBuffer = append(newBuffer, s.buffer...)
		if len(newBuffer) > maxBufferSize {
			// Keep the most recent samples
			newBuffer = newBuffer[len(newBuffer)-maxBufferSize:]
		}
		s.buffer = newBuffer
		s.mu.Unlock()
		return
	}

	slog.Info("pushed metrics", "samples", len(toSend))
}
