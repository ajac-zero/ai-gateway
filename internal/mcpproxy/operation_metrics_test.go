// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

type recordedOp struct {
	side, method string
	params       mcp.Params
	err          error
}

type opRecorder struct {
	mu  sync.Mutex
	ops []recordedOp
}

// opMetrics is a stubMetrics that also implements metrics.MCPOperationMetrics.
type opMetrics struct {
	stubMetrics
	rec *opRecorder
}

func (o opMetrics) WithBackend(string) metrics.MCPMetrics { return o }
func (o opMetrics) WithRequestAttributes(*http.Request) metrics.MCPMetrics {
	return o
}

func (o opMetrics) add(side, method string, p mcp.Params, err error) {
	o.rec.mu.Lock()
	defer o.rec.mu.Unlock()
	o.rec.ops = append(o.rec.ops, recordedOp{side, method, p, err})
}

func (o opMetrics) RecordClientOperationDuration(_ context.Context, _ time.Time, m string, p mcp.Params, err error) {
	o.add("client", m, p, err)
}

func (o opMetrics) RecordServerOperationDuration(_ context.Context, _ time.Time, m string, p mcp.Params, err error) {
	o.add("server", m, p, err)
}

func TestOperationMetrics_ServerRecordedOnPOSTCompletion(t *testing.T) {
	rec := &opRecorder{}
	proxy := newTestMCPProxy()
	proxy.metrics = opMetrics{rec: rec}

	params := &mcp.CallToolParams{Name: "tool"}
	boom := errors.New("boom")
	for _, perBackend := range []bool{false, true} {
		proxy.perBackendMetricsRecorded = perBackend
		proxy.recordPOSTCompletion(&postCompletion{ctx: t.Context(), method: "tools/call", params: params, err: boom, startAt: time.Now()})
	}
	// Requests without a method (e.g. client responses, undecodable bodies) are not operations.
	proxy.recordPOSTCompletion(&postCompletion{ctx: t.Context(), startAt: time.Now()})

	require.Len(t, rec.ops, 2)
	for _, op := range rec.ops {
		require.Equal(t, recordedOp{"server", "tools/call", params, boom}, op)
	}
}

func TestOperationMetrics_NoopAndCustomMetricsUnaffected(t *testing.T) {
	proxy := newTestMCPProxy()
	proxy.metrics = stubMetrics{} // does not implement MCPOperationMetrics
	require.NotPanics(t, func() {
		proxy.recordPOSTCompletion(&postCompletion{ctx: t.Context(), method: "tools/list", startAt: time.Now()})
		proxy.recordClientOperation(t.Context(), "b", time.Now(), "tools/list", nil, nil)
	})
}

func TestOperationMetrics_ModernDiscoverRecordsClientPerBackend(t *testing.T) {
	respFn := func(_, _ string) any {
		return mcp.DiscoverResult{SupportedVersions: []string{protocolVersion20260728}, Capabilities: &mcp.ServerCapabilities{}}
	}
	server := httptest.NewServer(modernBackendHandler(t, nil, map[string]bool{"backend2": true}, respFn))
	defer server.Close()

	rec := &opRecorder{}
	proxy := newTestMCPProxy()
	proxy.metrics = opMetrics{rec: rec}
	proxy.backendListenerAddr = server.URL
	_, err := proxy.handleServerDiscover(t.Context(), httptest.NewRecorder(), modernReq(t, "server/discover", nil), "test-route", nil)
	require.NoError(t, err)

	var okCount, errCount int
	for _, op := range rec.ops {
		require.Equal(t, "client", op.side)
		require.Equal(t, "server/discover", op.method)
		if op.err != nil {
			errCount++
		} else {
			okCount++
		}
	}
	require.Equal(t, 1, okCount)
	require.Equal(t, 1, errCount)
}

func TestOperationMetrics_LegacyFanoutRecordsClient(t *testing.T) {
	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)
	rec := &opRecorder{}
	proxy := newTestMCPProxy()
	proxy.metrics = opMetrics{rec: rec}
	s := &session{perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"a": {sessionID: "s"}}}

	type data struct{}
	rpcErr := &jsonrpc.Error{Code: -32000, Message: "x"}
	events := make(chan *backendEvent, 3)
	start := time.Now()
	for _, msg := range []jsonrpc.Message{
		&jsonrpc.Response{ID: reqID, Result: []byte(`{}`)},
		&jsonrpc.Response{ID: reqID, Result: []byte(`invalid`)},
		&jsonrpc.Response{ID: reqID, Error: rpcErr},
	} {
		events <- &backendEvent{sseEvent: &sseEvent{backend: "a", messages: []jsonrpc.Message{msg}}, startAt: start}
	}
	close(events)

	var params *mcp.ListToolsParams
	err = sendToAllBackendsAndAggregateResponsesImpl(t.Context(), events, proxy, httptest.NewRecorder(), s,
		&jsonrpc.Request{ID: reqID, Method: "tools/list"}, params,
		func(*session, []broadCastResponse[data]) data { return data{} }, nil)
	require.ErrorIs(t, err, errBackendResponseError)

	require.Len(t, rec.ops, 3)
	require.NoError(t, rec.ops[0].err)
	require.Error(t, rec.ops[1].err)
	require.Equal(t, error(rpcErr), rec.ops[2].err)
	for _, op := range rec.ops {
		require.Equal(t, "client", op.side)
		require.Equal(t, "tools/list", op.method)
	}
}

func TestOperationMetrics_FeedsOTELMetricNames(t *testing.T) {
	mr := sdkmetric.NewManualReader()
	proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
	t.Cleanup(func() { _ = mr.Shutdown(t.Context()) })

	proxy.recordPOSTCompletion(&postCompletion{ctx: t.Context(), method: "tools/call", params: &mcp.CallToolParams{Name: "t"}, startAt: time.Now().Add(-time.Millisecond)})
	proxy.recordClientOperation(t.Context(), "backend1", time.Now().Add(-time.Millisecond), "tools/call", &mcp.CallToolParams{Name: "t"}, nil)

	attrs := attribute.NewSet(attribute.String("mcp.method.name", "tools/call"), attribute.String("gen_ai.tool.name", "t"))
	for _, name := range []string{"mcp.server.operation.duration", "mcp.client.operation.duration"} {
		count, sum := testotel.GetHistogramValues(t, mr, name, attrs)
		require.Equal(t, uint64(1), count, name)
		require.Greater(t, sum, 0.0, name)
	}
	// Legacy series is unchanged.
	require.Equal(t, 1, int(testotel.GetCounterValue(t, mr, "mcp.method.count", attribute.NewSet(
		attribute.String("mcp.method.name", "tools/call"), attribute.String("status", "success")))))
}
