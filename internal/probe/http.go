package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/Handgrip/global-telemetry/internal/config"
)

var (
	secureTransport = &http.Transport{
		DisableKeepAlives:   true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	}
	insecureTransport = &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	}
)

type HTTPProbe struct{}

func (p *HTTPProbe) Type() string { return "http" }

func (p *HTTPProbe) Run(ctx context.Context, target config.Target, timeout time.Duration) (*RawResult, error) {
	targetURL := target.URL
	if targetURL == "" {
		return nil, fmt.Errorf("http target %q has no url", target.Name)
	}

	method := "GET"
	expectedStatus := 200
	skipTLS := false

	if target.HTTP != nil {
		if target.HTTP.Method != "" {
			method = target.HTTP.Method
		}
		if target.HTTP.ExpectedStatus > 0 {
			expectedStatus = target.HTTP.ExpectedStatus
		}
		skipTLS = target.HTTP.SkipTLSVerify
	}

	var dnsStart, dnsDone, connectStart, connectDone time.Time
	var tlsStart, tlsDone, gotFirstByte time.Time

	trace := &httptrace.ClientTrace{
		DNSStart:             func(_ httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(_ httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		ConnectStart:         func(_, _ string) { connectStart = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { connectDone = time.Now() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
		GotFirstResponseByte: func() { gotFirstByte = time.Now() },
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(timeoutCtx, trace),
		method, targetURL, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	transport := secureTransport
	if skipTLS {
		transport = insecureTransport
	}
	client := &http.Client{Transport: transport}

	start := time.Now()
	resp, err := client.Do(req)
	totalDuration := time.Since(start)

	if err != nil {
		return &RawResult{
			Target:    target,
			Timestamp: time.Now(),
			Success:   false,
			Error:     fmt.Sprintf("http request failed: %v", err),
			HTTP: &HTTPRawResult{
				TotalDuration: totalDuration,
			},
		}, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	var tlsExpiry time.Time
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		tlsExpiry = resp.TLS.PeerCertificates[0].NotAfter
	}

	httpResult := &HTTPRawResult{
		StatusCode:    resp.StatusCode,
		TotalDuration: totalDuration,
		TLSExpiry:     tlsExpiry,
	}
	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		httpResult.DNSDuration = dnsDone.Sub(dnsStart)
	}
	if !connectStart.IsZero() && !connectDone.IsZero() {
		httpResult.ConnDuration = connectDone.Sub(connectStart)
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		httpResult.TLSDuration = tlsDone.Sub(tlsStart)
	}
	if !gotFirstByte.IsZero() {
		httpResult.TTFBDuration = gotFirstByte.Sub(start)
	}

	return &RawResult{
		Target:    target,
		Timestamp: time.Now(),
		Success:   resp.StatusCode == expectedStatus,
		HTTP:      httpResult,
	}, nil
}
