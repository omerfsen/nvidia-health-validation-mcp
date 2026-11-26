// Copyright (c) 2026, Omer Sen.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const latestProtocol = "2026-07-28"

func TestHelpGoesToStdoutAndEnds(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		var stdout, stderr bytes.Buffer

		_, code, done := parseFlags([]string{flag}, &stdout, &stderr)
		assert.True(t, done)
		assert.Zero(t, code)
		assert.Contains(t, stdout.String(), "HV_MCP_ALLOW_EVALUATE")
		assert.Empty(t, stderr.String())
	}
}

func TestVersionGoesToStdoutAndEnds(t *testing.T) {
	for _, flag := range []string{"-v", "--version"} {
		var stdout bytes.Buffer

		_, code, done := parseFlags([]string{flag}, &stdout, io.Discard)
		assert.True(t, done)
		assert.Zero(t, code)
		assert.Contains(t, stdout.String(), "health-validation catalog v")
	}
}

func TestDebugRunsTheServer(t *testing.T) {
	for _, flag := range []string{"-d", "--debug"} {
		parsed, _, done := parseFlags([]string{flag}, io.Discard, io.Discard)
		assert.False(t, done)
		assert.True(t, parsed.debug)
	}

	parsed, _, done := parseFlags(nil, io.Discard, io.Discard)
	assert.False(t, done)
	assert.False(t, parsed.debug)
}

// With -d each call to serve leaves one debug line, without a body or an address.
func TestDebugLogsEachCallToServe(t *testing.T) {
	for _, debug := range []bool{true, false} {
		raw := startRawWith(t, options{debug: debug},
			map[string]string{"HV_MCP_SERVE_PORT": closedPort(t), "HV_MCP_ALLOW_EVALUATE": "true"})

		raw.send(t, 1, "tools/call", map[string]any{"name": "hv_service_status", "arguments": map[string]any{}})
		raw.reply(t, 1)
		raw.send(t, 2, "tools/call", map[string]any{"name": "hv_get_verdict", "arguments": map[string]any{}})
		raw.reply(t, 2)
		require.NoError(t, raw.stdin.Close())
		require.NoError(t, <-raw.exited)

		var calls []map[string]any

		for line := range strings.SplitSeq(raw.stderr.String(), "\n") {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "call to serve" {
				calls = append(calls, entry)
			}
		}

		if !debug {
			assert.Empty(t, calls)
			continue
		}

		require.Len(t, calls, 2, raw.stderr.String())
		assert.Equal(t, "DEBUG", calls[0]["level"])
		assert.Equal(t, "/healthz", calls[0]["path"])
		assert.Equal(t, "POST", calls[1]["method"])
		assert.Equal(t, "/verdict", calls[1]["path"])
		assert.Equal(t, "service_not_running", calls[1]["error_kind"])
		assert.NotContains(t, calls[1], "error", "serve's own text stays out")
	}
}

func TestABadCommandLineIsRefused(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"serve"}} {
		var stderr bytes.Buffer

		_, code, done := parseFlags(args, io.Discard, &stderr)
		assert.True(t, done)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "Usage:")
	}
}

func TestABadSettingStopsTheServer(t *testing.T) {
	for name, value := range map[string]string{
		"HV_MCP_SERVE_HOST":                "192.0.2.1",
		"HV_MCP_SERVE_PORT":                "22",
		"HV_MCP_EVALUATE_INTERVAL_SECONDS": "1",
	} {
		reader, writer := io.Pipe()
		err := run(options{}, func(key string) string {
			if key == name {
				return value
			}

			return ""
		}, reader, nopWriteCloser{io.Discard}, io.Discard)
		assert.Error(t, err, name)
		_ = writer.Close()
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// rawServer runs the server in this process on pipes and speaks raw JSON-RPC to it.
type rawServer struct {
	stdin   io.WriteCloser
	replies chan map[string]any
	stderr  *lockedBuffer
	exited  chan error
}

type lockedBuffer struct {
	buffer bytes.Buffer
	mu     sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

func closedPort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	return strconv.Itoa(port)
}

func startRaw(t *testing.T, env map[string]string) *rawServer {
	t.Helper()

	return startRawWith(t, options{}, env)
}

func startRawWith(t *testing.T, opts options, env map[string]string) *rawServer {
	t.Helper()

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	raw := &rawServer{
		stdin: stdinWriter, replies: make(chan map[string]any, 32), stderr: &lockedBuffer{}, exited: make(chan error, 1),
	}

	go func() {
		raw.exited <- run(opts, func(key string) string { return env[key] }, stdinReader, stdoutWriter, raw.stderr)
		_ = stdoutWriter.Close()
	}()

	t.Cleanup(func() { _ = stdinWriter.Close() })

	go func() {
		defer close(raw.replies)

		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)

		for scanner.Scan() {
			var reply map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
				reply = map[string]any{"notJSON": scanner.Text()}
			}

			raw.replies <- reply
		}
	}()

	return raw
}

func (r *rawServer) send(t *testing.T, id int, method string, params map[string]any) {
	t.Helper()

	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    latestProtocol,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}

	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(t, err)

	_, err = r.stdin.Write(append(line, '\n'))
	require.NoError(t, err)
}

func (r *rawServer) reply(t *testing.T, id int) map[string]any {
	t.Helper()

	select {
	case reply, open := <-r.replies:
		require.True(t, open, "the server closed stdout")
		require.Nil(t, reply["notJSON"], "stdout must hold only MCP messages")
		require.InDelta(t, id, reply["id"], 0)

		return reply
	case <-time.After(30 * time.Second):
		require.FailNow(t, "no reply", "request %d", id)
		return nil
	}
}

func resultOf(t *testing.T, reply map[string]any) map[string]any {
	t.Helper()

	result, ok := reply["result"].(map[string]any)
	require.True(t, ok, "reply: %v", reply)

	return result
}

func TestItSpeaksTheLatestProtocol(t *testing.T) {
	raw := startRaw(t, map[string]string{"HV_MCP_SERVE_PORT": closedPort(t), "HV_MCP_ALLOW_EVALUATE": "true"})

	raw.send(t, 1, "server/discover", map[string]any{})
	discover := resultOf(t, raw.reply(t, 1))
	assert.Equal(t, "complete", discover["resultType"])
	assert.Contains(t, discover["supportedVersions"], latestProtocol)
	meta, ok := discover["_meta"].(map[string]any)
	require.True(t, ok, "discover: %v", discover)
	assert.NotNil(t, meta["io.modelcontextprotocol/serverInfo"])
	assert.Equal(t, map[string]any{"tools": map[string]any{}}, discover["capabilities"])

	raw.send(t, 2, "tools/list", map[string]any{})
	first := resultOf(t, raw.reply(t, 2))
	assert.Equal(t, "complete", first["resultType"])
	assert.Contains(t, first, "ttlMs")
	assert.Contains(t, first, "cacheScope")
	assert.Len(t, first["tools"], 5)

	raw.send(t, 3, "tools/list", map[string]any{})
	assert.Equal(t, first["tools"], resultOf(t, raw.reply(t, 3))["tools"], "the order must be deterministic")

	raw.send(t, 4, "tools/call", map[string]any{"name": "hv_describe_check", "arguments": map[string]any{"check_id": "nope"}})
	assert.Equal(t, true, resultOf(t, raw.reply(t, 4))["isError"], "bad input is a tool execution error")

	raw.send(t, 5, "tools/call", map[string]any{"name": "does_not_exist", "arguments": map[string]any{}})
	unknown, ok := raw.reply(t, 5)["error"].(map[string]any)
	require.True(t, ok, "an unknown tool is a protocol error")
	assert.InDelta(t, -32602, unknown["code"], 0)

	raw.send(t, 6, "tools/call", map[string]any{"name": "hv_list_checks", "arguments": map[string]any{}})
	listing := resultOf(t, raw.reply(t, 6))
	assert.NotNil(t, listing["structuredContent"])
	assert.NotEmpty(t, listing["content"], "structured results also come as text")

	raw.send(t, 7, "tools/call", map[string]any{"name": "hv_get_verdict", "arguments": map[string]any{}})
	verdict := resultOf(t, raw.reply(t, 7))
	structured, ok := verdict["structuredContent"].(map[string]any)
	require.True(t, ok, "verdict: %v", verdict)
	assert.Equal(t, "NO_VERDICT", structured["decision"].(map[string]any)["scheduling_advice"],
		"no serve is running, so there is no verdict")

	require.NoError(t, raw.stdin.Close())

	select {
	case err := <-raw.exited:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "the server must exit when stdin closes")
	}

	logged := raw.stderr.String()
	assert.Contains(t, logged, "started: waiting for an MCP client")
	assert.Contains(t, logged, `"outcome":"not_running"`, "the evaluation attempt is audited on stderr")
}

// listeningSockets returns the inodes of TCP and Unix sockets in LISTEN state that this
// process holds.
func listeningSockets(t *testing.T) []string {
	t.Helper()

	held := map[string]bool{}

	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)

	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && strings.HasPrefix(target, "socket:[") {
			held[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}

	var listening []string

	for _, table := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(filepath.Join("/proc/self/net", table))
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) > 9 && fields[3] == "0A" && held[fields[9]] {
				listening = append(listening, table+":"+fields[1])
			}
		}
	}

	if raw, err := os.ReadFile("/proc/self/net/unix"); err == nil {
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			// Num RefCount Protocol Flags Type St Inode Path; flag 00010000 is __SO_ACCEPTCON.
			fields := strings.Fields(line)
			if len(fields) > 6 && fields[3] == "00010000" && held[fields[6]] {
				listening = append(listening, "unix:"+fields[6])
			}
		}
	}

	return listening
}

func TestNoListeningSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}

	require.Empty(t, listeningSockets(t), "the test process itself must not listen")

	// The detector must see a listener when there is one, or an empty result proves nothing.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	assert.NotEmpty(t, listeningSockets(t))
	require.NoError(t, probe.Close())
	require.Empty(t, listeningSockets(t))

	raw := startRaw(t, map[string]string{"HV_MCP_SERVE_PORT": closedPort(t), "HV_MCP_ALLOW_EVALUATE": "true"})

	raw.send(t, 1, "server/discover", map[string]any{})
	raw.reply(t, 1)

	for id, tool := range []string{"hv_service_status", "hv_get_verdict", "hv_list_checks"} {
		raw.send(t, id+2, "tools/call", map[string]any{"name": tool, "arguments": map[string]any{}})
		raw.reply(t, id+2)
	}

	assert.Empty(t, listeningSockets(t), "the server speaks stdio and opens no port")
}

// blockingServe answers /verdict only when the request is cancelled. It reports when a
// request arrives and when its context ends.
func blockingServe(t *testing.T) (port string, entered, cancelled chan struct{}) {
	t.Helper()

	entered, cancelled = make(chan struct{}), make(chan struct{})
	release := make(chan struct{})

	fake := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		_, _ = io.ReadAll(request.Body)

		close(entered)

		select {
		case <-request.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(func() { close(release) })

	_, port, err := net.SplitHostPort(fake.Listener.Addr().String())
	require.NoError(t, err)

	return port, entered, cancelled
}

// stopsTheEvaluation starts an evaluation that serve never answers, runs stop, and
// checks that serve saw the request cancelled and the server exited.
func stopsTheEvaluation(t *testing.T, stop func(*rawServer)) {
	t.Helper()

	port, entered, cancelled := blockingServe(t)

	raw := startRaw(t, map[string]string{"HV_MCP_SERVE_PORT": port, "HV_MCP_ALLOW_EVALUATE": "true"})
	raw.send(t, 1, "tools/call", map[string]any{"name": "hv_get_verdict", "arguments": map[string]any{}})

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		require.FailNow(t, "the evaluation never reached serve")
	}

	stop(raw)

	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the running evaluation must be cancelled")
	}

	select {
	case err := <-raw.exited:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "the server must exit")
	}

	assert.Contains(t, raw.stderr.String(), `"outcome":"cancelled"`)
}

// Serve never answers, so only a cancellation ends these evaluations early.
func TestStdinCloseCancelsEvaluate(t *testing.T) {
	stopsTheEvaluation(t, func(raw *rawServer) { require.NoError(t, raw.stdin.Close()) })
}

// On stdio the SDK waits for running handlers when its context ends but does not cancel
// them, so the server's handlers follow their own shutdown context.
func TestSigtermCancelsEvaluate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM")
	}

	stopsTheEvaluation(t, func(*rawServer) {
		// run has registered for SIGTERM, so the signal reaches it, not the test binary's default.
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
	})
}
