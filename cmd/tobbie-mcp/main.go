// Command tobbie-mcp exposes the Tobbie II robot as a Model Context
// Protocol server (stdio transport) so that AI agents can drive the
// robot directly: scan, connect, move, faces, scrolling text.
//
// Unlike the tobbie CLI, the server opens ONE BLE link at startup and
// keeps it alive across tool calls; if the link drops (robot reboot,
// range) it reconnects lazily before the next call. Set TOBBIE_SIM=1
// to run against an in-memory mock that reports the exact wire bytes
// instead of hardware.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/mheers/tobbie2/internal/tobbie"
)

const (
	serverName    = "tobbie"
	serverVersion = "0.2.0"
	connectTries  = 3
)

func main() {
	tobbie.ApplyStepDurationEnv()
	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
	)

	rs := newRobotServer()
	rs.start()
	defer rs.close()

	registerTools(s, rs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stdio := server.NewStdioServer(s)
	err := stdio.Listen(ctx, os.Stdin, os.Stdout)
	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "tobbie-mcp: %v\n", err)
		os.Exit(1)
	}
}

// robotServer owns the persistent BLE link to the robot.
type robotServer struct {
	mu    sync.Mutex
	sim   bool
	addr  string
	robot *tobbie.BLE
	mock  *tobbie.Mock
}

func newRobotServer() *robotServer {
	rs := &robotServer{sim: os.Getenv("TOBBIE_SIM") != ""}
	if !rs.sim {
		rs.robot = tobbie.NewBLE()
		if cfg, err := tobbie.LoadConfig(); err == nil {
			rs.addr = cfg.MAC
		}
	}
	return rs
}

// start opens the BLE link to the saved robot during startup. A
// failure is logged, not fatal: every call retries the connection
// lazily.
func (rs *robotServer) start() {
	if rs.sim {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := rs.ensure(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "tobbie-mcp: startup connect failed (will retry on demand): %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "tobbie-mcp: connected to %s\n", rs.addr)
		}
	}()
}

// close tears the link down on shutdown.
func (rs *robotServer) close() {
	if rs.sim {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.robot != nil {
		_ = rs.robot.Disconnect()
	}
}

// ensure returns with a live link to rs.addr, reconnecting if the
// link is down (with retries and backoff).
func (rs *robotServer) ensure(ctx context.Context) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.ensureLocked(ctx)
}

func (rs *robotServer) ensureLocked(ctx context.Context) error {
	if rs.sim {
		return nil
	}
	if rs.robot.Connected() {
		return nil
	}
	if rs.addr == "" {
		return fmt.Errorf("no robot address: run scan, then connect with mac")
	}
	var lastErr error
	for i := 0; i < connectTries; i++ {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := rs.robot.Connect(cctx, rs.addr)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return lastErr
}

// run executes one robot command over the persistent link (or the
// mock in sim mode, appending the wire bytes to the result). The
// mutex is held for the whole call so commands from concurrent tool
// calls are serialized (a multi-step move must not be interleaved
// with another command).
func (rs *robotServer) run(ctx context.Context, fn func(tobbie.Robot) (string, error)) (*mcp.CallToolResult, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if rs.sim {
		if rs.mock == nil {
			rs.mock = tobbie.NewMock()
			_ = rs.mock.Connect(context.Background(), rs.addr)
		}
		start := len(rs.mock.Commands)
		msg, err := fn(rs.mock)
		if err != nil {
			return nil, err
		}
		if n := len(rs.mock.Commands) - start; n > 0 {
			cmds := make([]string, 0, n)
			for _, c := range rs.mock.Commands[start:] {
				cmds = append(cmds, string(c))
			}
			msg += fmt.Sprintf("\nmock wire: %q", strings.Join(cmds, ", "))
		}
		return textResult(msg), nil
	}

	if err := rs.ensureLocked(ctx); err != nil {
		return nil, err
	}
	msg, err := fn(rs.robot)
	if err != nil {
		return nil, err
	}
	return textResult(msg), nil
}

// --- tool definitions ----------------------------------------------

func registerTools(s *server.MCPServer, rs *robotServer) {
	s.AddTool(scanTool(), rs.handleScan)
	s.AddTool(connectTool(), rs.handleConnect)
	s.AddTool(statusTool(), rs.handleStatus)
	s.AddTool(moveTool(), rs.handleMove)
	s.AddTool(faceTool(), rs.handleFace)
	s.AddTool(faceSetTool(), rs.handleFaceSet)
	s.AddTool(textTool(), rs.handleText)
	s.AddTool(stopSignTool(), rs.handleStopSign)
	s.AddTool(disconnectTool(), rs.handleDisconnect)
}

func scanTool() mcp.Tool {
	return mcp.NewTool("scan",
		mcp.WithDescription("Scan for nearby BLE devices. The Tobbie II robot advertises as a BBC micro:bit; use name to narrow the list."),
		mcp.WithString("name", mcp.Description("optional substring filter on the advertised device name")),
	)
}

func connectTool() mcp.Tool {
	return mcp.NewTool("connect",
		mcp.WithDescription("Connect to the robot, remember its address and keep the link open. Omit mac to use the saved address."),
		mcp.WithString("mac", mcp.Description("BLE address of the robot, e.g. ec:11:f9:d9:4f:5e")),
	)
}

func statusTool() mcp.Tool {
	return mcp.NewTool("status",
		mcp.WithDescription("Show the saved robot address and the current state of the persistent BLE link."),
	)
}

func moveTool() mcp.Tool {
	return mcp.NewTool("move",
		mcp.WithDescription("Send a movement command. Directions: "+strings.Join(directionNames(), ", ")+" (raw wire codes a0..a8 also accepted). By default the robot keeps moving until a stop command; pass steps (e.g. one discrete step, or 5 steps) or duration_ms (e.g. a short turn) to move for a limited time and stop automatically."),
		mcp.WithString("direction", mcp.Required(), mcp.Description("one of: "+strings.Join(directionNames(), ", "))),
		mcp.WithNumber("steps", mcp.Description("number of discrete steps; each step holds the movement for ~625ms before stopping. Mutually exclusive with duration_ms.")),
		mcp.WithNumber("duration_ms", mcp.Description(fmt.Sprintf("how long to hold the movement in milliseconds before stopping (fine-grained turns). Calibrated on the robot: %d ms = a 180° turn in place (90° ≈ %d ms); scale proportionally for other angles. Mutually exclusive with steps.", tobbie.TurnCalibration180/time.Millisecond, tobbie.TurnCalibration180/time.Millisecond/2))),
	)
}

func faceTool() mcp.Tool {
	return mcp.NewTool("face",
		mcp.WithDescription("Show one of the nine built-in faces on the robot's LED matrix."),
		mcp.WithString("number", mcp.Required(), mcp.Description("face number 1..9")),
	)
}

func faceSetTool() mcp.Tool {
	return mcp.NewTool("face-set",
		mcp.WithDescription("Draw a custom face on the 5x5 LED matrix. Two formats: a grid of five rows of five '#'/'.' chars separated by '|', or five decimal row values 0..31 (each row is a bitmask, bit 0 = leftmost LED)."),
		mcp.WithString("grid", mcp.Required(), mcp.Description("e.g. '#...#|#...#|#####|#...#|#...#' or '17 17 31 17 17'")),
	)
}

func textTool() mcp.Tool {
	return mcp.NewTool("text",
		mcp.WithDescription("Scroll a text message across the robot's LED matrix."),
		mcp.WithString("message", mcp.Required(), mcp.Description("the message to scroll")),
	)
}

func stopSignTool() mcp.Tool {
	return mcp.NewTool("stop-sign",
		mcp.WithDescription("Show the stop pictogram on the robot's LED matrix."),
	)
}

func disconnectTool() mcp.Tool {
	return mcp.NewTool("disconnect",
		mcp.WithDescription("Close the BLE link (it is reopened automatically before the next call)."),
	)
}

// --- handlers ------------------------------------------------------

func (rs *robotServer) handleScan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filter, _ := req.GetArguments()["name"].(string)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Prefer scanning on the live link's adapter (a second HCI device
	// would tear the link down); fall back to a fresh device when
	// disconnected.
	if !rs.sim && rs.robot.Connected() {
		hits, err := rs.robot.Scan(sctx, filter)
		if err != nil {
			return nil, err
		}
		return textResult(formatHits(hits)), nil
	}
	hits, err := tobbie.Scan(sctx, filter)
	if err != nil {
		return nil, err
	}
	return textResult(formatHits(hits)), nil
}

func formatHits(hits []tobbie.DeviceHit) string {
	if len(hits) == 0 {
		return "no devices found"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-30s %-18s %6s\n", "NAME", "ADDRESS", "RSSI")
	for _, h := range hits {
		fmt.Fprintf(&b, "%-30s %-18s %6d\n", h.Name, h.Addr, h.RSSI)
	}
	return b.String()
}

func (rs *robotServer) handleConnect(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if rs.sim {
		return textResult("sim mode: connection is a no-op"), nil
	}
	mac, _ := req.GetArguments()["mac"].(string)
	addr := mac
	if addr == "" {
		cfg, err := tobbie.LoadConfig()
		if err != nil {
			return nil, err
		}
		addr = cfg.MAC
		if addr == "" {
			return nil, fmt.Errorf("no address: run scan, pick the micro:bit, then connect with mac")
		}
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()
	_ = rs.robot.Disconnect() // repoint an existing link
	rs.addr = addr
	if err := rs.ensureLocked(ctx); err != nil {
		return nil, err
	}
	cfg := tobbie.Config{MAC: addr}
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return textResult(fmt.Sprintf("connected to %s (saved, link kept open)", addr)), nil
}

func (rs *robotServer) handleStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if rs.sim {
		return textResult("sim mode"), nil
	}
	state := "not connected"
	rs.mu.Lock()
	if rs.robot.Connected() {
		state = "connected"
	}
	rs.mu.Unlock()
	addr := rs.addr
	if addr == "" {
		if cfg, err := tobbie.LoadConfig(); err == nil {
			addr = cfg.MAC
		}
	}
	if addr == "" {
		return textResult("no robot configured (run scan + connect first)"), nil
	}
	return textResult(fmt.Sprintf("configured robot: %s\nlink: %s", addr, state)), nil
}

func (rs *robotServer) handleMove(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir, err := str("direction", req)
	if err != nil {
		return nil, err
	}
	d, err := tobbie.ParseDirection(dir)
	if err != nil {
		return nil, err
	}

	steps, hasSteps := intOpt("steps", req)
	dur, hasDur := intOpt("duration_ms", req)
	if hasSteps && hasDur {
		return nil, fmt.Errorf("provide either steps or duration_ms, not both")
	}

	// stop is immediate regardless of any requested duration.
	if d == tobbie.DirStop {
		return rs.run(ctx, func(r tobbie.Robot) (string, error) {
			if err := r.Move(d); err != nil {
				return "", err
			}
			return "move: stop", nil
		})
	}

	switch {
	case hasSteps:
		if steps < 0 {
			return nil, fmt.Errorf("steps must be >= 0, got %d", steps)
		}
		return rs.run(ctx, func(r tobbie.Robot) (string, error) {
			if err := r.Step(ctx, d, steps); err != nil {
				return "", err
			}
			return fmt.Sprintf("move: %s (%d steps)", d, steps), nil
		})
	case hasDur:
		if dur < 0 {
			return nil, fmt.Errorf("duration_ms must be >= 0, got %d", dur)
		}
		pulse := time.Duration(dur) * time.Millisecond
		return rs.run(ctx, func(r tobbie.Robot) (string, error) {
			if err := r.MoveFor(ctx, d, pulse); err != nil {
				return "", err
			}
			return fmt.Sprintf("move: %s (%.0fms)", d, float64(dur)), nil
		})
	default:
		return rs.run(ctx, func(r tobbie.Robot) (string, error) {
			if err := r.Move(d); err != nil {
				return "", err
			}
			return fmt.Sprintf("move: %s", d), nil
		})
	}
}

func (rs *robotServer) handleFace(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	n, err := str("number", req)
	if err != nil {
		return nil, err
	}
	f, err := tobbie.ParseFace(n)
	if err != nil {
		return nil, err
	}
	return rs.run(ctx, func(r tobbie.Robot) (string, error) {
		if err := r.SetFace(f); err != nil {
			return "", err
		}
		return fmt.Sprintf("face: %d", f), nil
	})
}

func (rs *robotServer) handleFaceSet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	grid, err := str("grid", req)
	if err != nil {
		return nil, err
	}
	rows, err := tobbie.ParseFaceGrid(grid)
	if err != nil {
		return nil, err
	}
	return rs.run(ctx, func(r tobbie.Robot) (string, error) {
		if err := r.DrawFace(rows); err != nil {
			return "", err
		}
		return fmt.Sprintf("face-set: %v", rows), nil
	})
}

func (rs *robotServer) handleText(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	msg, err := str("message", req)
	if err != nil {
		return nil, err
	}
	return rs.run(ctx, func(r tobbie.Robot) (string, error) {
		if err := r.ShowText(msg); err != nil {
			return "", err
		}
		return fmt.Sprintf("text: %s", msg), nil
	})
}

func (rs *robotServer) handleStopSign(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return rs.run(ctx, func(r tobbie.Robot) (string, error) {
		if err := r.ShowStopSign(); err != nil {
			return "", err
		}
		return "stop-sign shown", nil
	})
}

func (rs *robotServer) handleDisconnect(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if rs.sim {
		return textResult("sim mode: nothing to disconnect"), nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	_ = rs.robot.Disconnect()
	return textResult("link closed (reopened automatically before the next call)"), nil
}

// --- plumbing ------------------------------------------------------

func str(name string, req mcp.CallToolRequest) (string, error) {
	v, ok := req.GetArguments()[name].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("missing argument %q", name)
	}
	return v, nil
}

// intOpt reads an optional numeric argument (JSON numbers arrive as
// float64). ok reports whether the argument was present.
func intOpt(name string, req mcp.CallToolRequest) (int, bool) {
	v, ok := req.GetArguments()[name].(float64)
	if !ok {
		return 0, false
	}
	return int(v), true
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(s)},
	}
}

func directionNames() []string {
	names := make([]string, len(tobbie.AllDirections))
	for i, d := range tobbie.AllDirections {
		names[i] = string(d)
	}
	return names
}
