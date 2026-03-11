package probe

import (
	"context"
	"fmt"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/Handgrip/global-telemetry/internal/config"
)

type ICMPProbe struct{}

func (p *ICMPProbe) Type() string { return "icmp" }

func (p *ICMPProbe) Run(ctx context.Context, target config.Target, timeout time.Duration) (*RawResult, error) {
	host := target.Host
	if host == "" {
		return nil, fmt.Errorf("icmp target %q has no host", target.Name)
	}

	count := 3
	if target.ICMP != nil && target.ICMP.Count > 0 {
		count = target.ICMP.Count
	}

	pinger, err := probing.NewPinger(host)
	if err != nil {
		return &RawResult{
			Target:    target,
			Timestamp: time.Now(),
			Success:   false,
			Error:     fmt.Sprintf("create pinger: %v", err),
			ICMP:      &ICMPRawResult{PacketsSent: 0, PacketsRecv: 0},
		}, nil
	}

	pinger.Count = count
	pinger.Timeout = timeout
	pinger.SetPrivileged(true)

	var runErr error
	if err := pinger.RunWithContext(ctx); err != nil {
		// Privileged raw ICMP failed (likely missing root/CAP_NET_RAW).
		// Check if we still have time to retry
		if ctx.Err() != nil {
			return &RawResult{
				Target:    target,
				Timestamp: time.Now(),
				Success:   false,
				Error:     fmt.Sprintf("ping failed: %v, no time for retry: %v", err, ctx.Err()),
				ICMP:      &ICMPRawResult{PacketsSent: 0, PacketsRecv: 0},
			}, nil
		}
		
		// Retry with unprivileged UDP-based ping.
		pinger, err = probing.NewPinger(host)
		if err != nil {
			return &RawResult{
				Target:    target,
				Timestamp: time.Now(),
				Success:   false,
				Error:     fmt.Sprintf("create unprivileged pinger: %v", err),
				ICMP:      &ICMPRawResult{PacketsSent: 0, PacketsRecv: 0},
			}, nil
		}
		pinger.Count = count
		pinger.Timeout = timeout
		pinger.SetPrivileged(false)
		runErr = pinger.RunWithContext(ctx)
	}

	stats := pinger.Statistics()
	result := &RawResult{
		Target:    target,
		Timestamp: time.Now(),
		Success:   stats.PacketsRecv > 0,
		ICMP: &ICMPRawResult{
			PacketRTTs:  stats.Rtts,
			PacketsSent: stats.PacketsSent,
			PacketsRecv: stats.PacketsRecv,
		},
	}
	if runErr != nil {
		result.Error = fmt.Sprintf("ping failed: %v", runErr)
		result.Success = false
	}
	return result, nil
}
