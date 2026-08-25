//go:build e2e

// Package e2e holds the hand-run smoke suite. it exercises mcp-tools,
// mcp-bridge, and mcp-gateway as real processes over real zrok shares and
// Agora tunnels. see docs/current/smoke-suite.md.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// namePrefix marks every fabric resource this suite creates. teardown
	// refuses to delete anything without it.
	namePrefix = "mcpe2e-"

	// startTimeout bounds how long a server may take to reach the fabric.
	// zrok share creation alone runs several seconds.
	startTimeout = 90 * time.Second

	// dialTimeout bounds the client-side retry loop against a server that is
	// up but whose fabric listener is not yet reachable.
	dialTimeout = 90 * time.Second

	// stopTimeout bounds graceful shutdown after SIGINT.
	stopTimeout = 45 * time.Second
)

// runID scopes every resource name to a single suite run.
var runID = newRunID()

func newRunID() string {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()&0xffffff, 16)
	}
	return hex.EncodeToString(buf)
}

// resourceName builds a run-scoped fabric resource name. zrok share tokens
// accept 3-32 characters of [a-z0-9-], which bounds the role string.
func resourceName(role string) string {
	name := namePrefix + runID + "-" + role
	if len(name) > 32 {
		panic(fmt.Sprintf("resource name %q exceeds the 32 character zrok limit", name))
	}
	return name
}

// -----------------------------------------------------------------------------
// binaries
// -----------------------------------------------------------------------------

var binDir string

// buildBinaries compiles the working tree into a scratch directory. the suite
// deliberately never uses whatever happens to be installed in GOPATH/bin: the
// point is to smoke the tree in front of you.
func buildBinaries(root string) (string, error) {
	dir, err := os.MkdirTemp("", "mcp-e2e-bin-")
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", dir, "./cmd/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("go build ./cmd/... failed: %v\n%s", err, out)
	}
	return dir, nil
}

func binPath(name string) string { return filepath.Join(binDir, name) }

// -----------------------------------------------------------------------------
// process control
// -----------------------------------------------------------------------------

// capture is a concurrency-safe sink for a child process stream.
type capture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// process is a running mcp-bridge or mcp-gateway under test.
type process struct {
	name    string
	cmd     *exec.Cmd
	stdout  *capture
	stderr  *capture
	tokenCh chan string
	waitCh  chan error
	stopped bool
}

// start launches a server binary from the built tree.
func start(t *testing.T, name string, args ...string) *process {
	t.Helper()

	cmd := exec.Command(binPath(name), args...)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("%s: stdout pipe: %v", name, err)
	}
	p := &process{
		name:    name,
		cmd:     cmd,
		stdout:  &capture{},
		stderr:  &capture{},
		tokenCh: make(chan string, 1),
		waitCh:  make(chan error, 1),
	}
	cmd.Stderr = p.stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: start: %v", name, err)
	}
	go p.scanStdout(stdoutPipe)
	go func() { p.waitCh <- cmd.Wait() }()

	t.Logf("started %s (pid %d): %s", name, cmd.Process.Pid, strings.Join(args, " "))
	t.Cleanup(func() { p.stop(t) })
	return p
}

// scanStdout tees the child's stdout into the capture buffer while decoding
// the JSON value stream looking for the share-token object.
//
// mcp-bridge and mcp-gateway currently write structured logs to stdout
// alongside the token, and the token object itself is pretty-printed across
// several lines, so a line-oriented scan does not work. a streaming JSON
// decoder handles both shapes; log records are distinguished by their "level"
// field. see docs/future/roadmap/token-output-shares-stdout-with-logs.md.
func (p *process) scanStdout(r io.Reader) {
	tee := io.TeeReader(r, p.stdout)
	dec := json.NewDecoder(bufio.NewReader(tee))
	for {
		var value map[string]any
		if err := dec.Decode(&value); err != nil {
			// not JSON any more (or EOF); keep draining so the child never
			// blocks on a full pipe, and keep capturing for diagnostics.
			io.Copy(io.Discard, tee)
			return
		}
		if _, isLog := value["level"]; isLog {
			continue
		}
		if token, ok := value["share_token"].(string); ok && token != "" {
			select {
			case p.tokenCh <- token:
			default:
			}
		}
	}
}

// shareToken waits for the share token the server prints on startup.
func (p *process) shareToken(t *testing.T) string {
	t.Helper()
	select {
	case token := <-p.tokenCh:
		t.Logf("%s: share token %s", p.name, token)
		return token
	case err := <-p.waitCh:
		t.Fatalf("%s exited before printing a share token (%v)\n%s", p.name, err, p.diagnostics())
	case <-time.After(startTimeout):
		t.Fatalf("%s did not print a share token within %s\n%s", p.name, startTimeout, p.diagnostics())
	}
	return ""
}

// awaitRunning gives a server without a share token (Agora serving) a chance
// to fail fast before the client retry loop starts.
func (p *process) awaitRunning(t *testing.T, grace time.Duration) {
	t.Helper()
	select {
	case err := <-p.waitCh:
		t.Fatalf("%s exited during startup (%v)\n%s", p.name, err, p.diagnostics())
	case <-time.After(grace):
	}
}

// stop sends SIGINT and waits for graceful shutdown, escalating if needed.
func (p *process) stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true

	if p.cmd.Process == nil {
		return
	}
	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		return // already gone
	}
	select {
	case <-p.waitCh:
	case <-time.After(stopTimeout):
		t.Errorf("%s did not exit within %s of SIGINT; killing\n%s", p.name, stopTimeout, p.diagnostics())
		p.cmd.Process.Kill()
		<-p.waitCh
	}
}

// kill terminates the process without letting it clean up. used to simulate a
// client or server that disappears.
func (p *process) kill(t *testing.T) {
	t.Helper()
	if p.stopped || p.cmd.Process == nil {
		return
	}
	p.stopped = true
	p.cmd.Process.Kill()
	<-p.waitCh
}

func (p *process) pid() int { return p.cmd.Process.Pid }

func (p *process) diagnostics() string {
	return fmt.Sprintf("--- %s stdout ---\n%s\n--- %s stderr ---\n%s", p.name, tail(p.stdout.String(), 40), p.name, tail(p.stderr.String(), 40))
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// -----------------------------------------------------------------------------
// mcp clients
// -----------------------------------------------------------------------------

// clientImpl identifies this suite to the servers it connects to.
var clientImpl = &mcp.Implementation{Name: "mcp-e2e", Version: "1.0.0"}

// toolsSession is an MCP session established through an mcp-tools child.
type toolsSession struct {
	*mcp.ClientSession
	stderr *capture
	pid    int
}

// dialViaTools runs `mcp-tools run <args...>` and speaks MCP to it over stdio,
// which is exactly how an agent consumes the trifecta. the fabric listener can
// lag the server's startup, so connection attempts are retried.
func dialViaTools(t *testing.T, args ...string) *toolsSession {
	t.Helper()

	deadline := time.Now().Add(dialTimeout)
	var lastErr error
	var lastStderr string
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		stderr := &capture{}
		cmd := exec.Command(binPath("mcp-tools"), append([]string{"run"}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Stderr = stderr

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		client := mcp.NewClient(clientImpl, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		cancel()
		if err == nil {
			t.Logf("mcp-tools run %s: connected on attempt %d", strings.Join(args, " "), attempt)
			ts := &toolsSession{ClientSession: session, stderr: stderr}
			if cmd.Process != nil {
				ts.pid = cmd.Process.Pid
			}
			t.Cleanup(func() { session.Close() })
			return ts
		}
		lastErr, lastStderr = err, stderr.String()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("mcp-tools run %s never connected within %s: %v\n--- mcp-tools stderr ---\n%s",
		strings.Join(args, " "), dialTimeout, lastErr, tail(lastStderr, 30))
	return nil
}

// dialHTTP speaks Streamable HTTP to a local endpoint, which is how an
// HTTP-only client consumes `mcp-tools http`.
func dialHTTP(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()

	deadline := time.Now().Add(dialTimeout)
	var lastErr error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		client := mcp.NewClient(clientImpl, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
		cancel()
		if err == nil {
			t.Logf("%s: connected on attempt %d", endpoint, attempt)
			t.Cleanup(func() { session.Close() })
			return session
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no streamable HTTP session at %s within %s: %v", endpoint, dialTimeout, lastErr)
	return nil
}

// -----------------------------------------------------------------------------
// mcp assertions
// -----------------------------------------------------------------------------

// toolNames lists the tools a session advertises.
func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func requireTools(t *testing.T, names []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !contains(names, w) {
			t.Fatalf("expected tool %q in %v", w, names)
		}
	}
}

func requireNoTool(t *testing.T, names []string, unwanted string) {
	t.Helper()
	if contains(names, unwanted) {
		t.Fatalf("tool %q should have been filtered out of %v", unwanted, names)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// readFile calls the named read_file tool and returns its text content. this
// is the round trip that proves the whole path carries a real payload, not
// just a successful handshake.
func readFile(t *testing.T, session *mcp.ClientSession, tool, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: map[string]any{"path": path},
	})
	if err != nil {
		t.Fatalf("tools/call %s: %v", tool, err)
	}
	if result.IsError {
		t.Fatalf("tools/call %s returned an error result: %+v", tool, result.Content)
	}
	var sb strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String()
}

// -----------------------------------------------------------------------------
// sandboxes
// -----------------------------------------------------------------------------

// sandbox creates a temp directory holding a known file, and returns the
// directory plus the file path and its contents.
func sandbox(t *testing.T, label string) (dir, file, contents string) {
	t.Helper()
	dir = t.TempDir()
	contents = fmt.Sprintf("mcp-gateway e2e %s %s\n", label, runID)
	file = filepath.Join(dir, "greeting.txt")
	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		t.Fatalf("sandbox %s: %v", label, err)
	}
	return dir, file, contents
}

// freePort reserves an ephemeral loopback port and releases it for the child
// to bind.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// -----------------------------------------------------------------------------
// child process accounting
// -----------------------------------------------------------------------------

// backendChildren returns the pids of mcp-filesystem processes parented by
// ppid. per-client backend subprocesses are the observable proof that a
// Streamable HTTP session did or did not release its resources.
func backendChildren(t *testing.T, ppid int) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		comm, parent, ok := procStat(pid)
		if !ok || parent != ppid || comm != "mcp-filesystem" {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// procStat reads a process's comm and ppid. comm may contain spaces and
// parentheses, so the fields after the final ')' are what can be split.
func procStat(pid int) (comm string, ppid int, ok bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, false
	}
	s := string(data)
	open := strings.Index(s, "(")
	closeIdx := strings.LastIndex(s, ")")
	if open < 0 || closeIdx < open {
		return "", 0, false
	}
	comm = s[open+1 : closeIdx]
	fields := strings.Fields(s[closeIdx+1:])
	if len(fields) < 2 {
		return "", 0, false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, false
	}
	return comm, ppid, true
}

// awaitBackendChildren polls until the backend subprocess count reaches want.
func awaitBackendChildren(t *testing.T, ppid, want int, within time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	var got int
	for time.Now().Before(deadline) {
		got = len(backendChildren(t, ppid))
		if got == want {
			t.Logf("%s: backend subprocess count reached %d", what, want)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s: expected %d backend subprocesses within %s, still have %d", what, want, within, got)
}

// abandon kills the mcp-tools child outright, so the server never sees a
// session termination. this is how a real client disappears — a laptop lid,
// a killed agent — and it is what the idle timeout exists to bound.
func (ts *toolsSession) abandon(t *testing.T) {
	t.Helper()
	if ts.pid == 0 {
		t.Fatalf("no mcp-tools pid was recorded for this session")
	}
	proc, err := os.FindProcess(ts.pid)
	if err != nil {
		t.Fatalf("find mcp-tools %d: %v", ts.pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill mcp-tools %d: %v", ts.pid, err)
	}
	t.Logf("abandoned mcp-tools (pid %d) without terminating its MCP session", ts.pid)
}
