package probe

import (
	"context"
	"time"

	"github.com/Handgrip/global-telemetry/internal/config"
)

// Probe executes a network check against a target and returns raw results.
type Probe interface {
	Type() string
	Run(ctx context.Context, target config.Target, timeout time.Duration) (*RawResult, error)
}

// RawResult holds the unprocessed output from a single probe execution.
// The reporter decides how to aggregate/serialize these.
type RawResult struct {
	Target    config.Target
	Timestamp time.Time
	Success   bool
	Error     string
	ICMP      *ICMPRawResult
	HTTP      *HTTPRawResult
}

// ICMPRawResult stores per-packet data from an ICMP probe cycle.
type ICMPRawResult struct {
	PacketRTTs  []time.Duration
	PacketsSent int
	PacketsRecv int
}

// HTTPRawResult stores timing breakdown from an HTTP probe cycle.
type HTTPRawResult struct {
	StatusCode    int
	DNSDuration   time.Duration
	ConnDuration  time.Duration
	TLSDuration   time.Duration
	TTFBDuration  time.Duration
	TotalDuration time.Duration
	TLSExpiry     time.Time
}

// ForType returns the appropriate Probe implementation for a target type.
func ForType(targetType string) Probe {
	switch targetType {
	case "icmp":
		return &ICMPProbe{}
	case "http", "https":
		return &HTTPProbe{}
	default:
		return nil
	}
}
