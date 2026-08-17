// Command tobbie is a CLI for driving the Velleman KSR20 "Tobbie II"
// robot (a BBC micro:bit v2 running the stock MakeCode Bluetooth
// remote-control firmware) over BLE.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mheers/tobbie2/internal/tobbie"
)

var (
	flagMAC     string
	flagSim     bool
	flagTimeout time.Duration
)

func main() {
	tobbie.ApplyStepDurationEnv()
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "tobbie",
		Short:         "Control the Velleman KSR20 Tobbie II robot over BLE",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flagMAC, "mac", "", "robot BLE address (MAC); falls back to the saved one")
	root.PersistentFlags().BoolVar(&flagSim, "sim", false, "use the in-memory mock robot instead of BLE")
	root.PersistentFlags().DurationVar(&flagTimeout, "timeout", 15*time.Second, "connect timeout")

	root.AddCommand(
		newScanCmd(),
		newConnectCmd(),
		newStatusCmd(),
		newMoveCmd(),
		newFaceCmd(),
		newFaceSetCmd(),
		newTextCmd(),
		newStopSignCmd(),
		newDisconnectCmd(),
	)
	return root
}

// withRobot connects (or mock-connects) and runs fn, then closes the
// link. It returns fn's error.
func withRobot(ctx context.Context, fn func(tobbie.Robot) error) error {
	var r tobbie.Robot
	if flagSim {
		r = tobbie.NewMock()
	} else {
		r = tobbie.NewBLE()
	}
	defer r.Close()

	addr := flagMAC
	if addr == "" && !flagSim {
		cfg, err := tobbie.LoadConfig()
		if err != nil {
			return err
		}
		addr = cfg.MAC
	}
	if addr == "" && !flagSim {
		return fmt.Errorf("no robot address: pass --mac or run `tobbie connect` first")
	}

	ctx, cancel := context.WithTimeout(ctx, flagTimeout)
	defer cancel()
	if err := r.Connect(ctx, addr); err != nil {
		return err
	}
	return fn(r)
}

// --- scan ---------------------------------------------------------

func newScanCmd() *cobra.Command {
	var filter string
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for nearby BLE devices",
		Long:  "Scan for nearby BLE devices. The Tobbie II advertises as a BBC micro:bit; use --name to narrow the list.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagSim {
				return fmt.Errorf("scan is not available in --sim mode")
			}

			ctx, cancel := context.WithTimeout(context.Background(), flagTimeout)
			defer cancel()
			hits, err := tobbie.Scan(ctx, filter)
			if err != nil {
				return err
			}
			if len(hits) == 0 {
				fmt.Println("no devices found")
				return nil
			}
			fmt.Printf("%-30s %-18s %6s\n", "NAME", "ADDRESS", "RSSI")
			for _, h := range hits {
				fmt.Printf("%-30s %-18s %6d\n", h.Name, h.Addr, h.RSSI)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filter, "name", "", "only show devices whose name contains this substring")
	return cmd
}

// --- connect ------------------------------------------------------

func newConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect",
		Short: "Connect to the robot and remember its address",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagSim {
				fmt.Println("sim mode: connection is a no-op")
				return nil
			}
			addr := flagMAC
			if addr == "" {
				cfg, err := tobbie.LoadConfig()
				if err != nil {
					return err
				}
				addr = cfg.MAC
			}
			if addr == "" {
				return fmt.Errorf("no address: run `tobbie scan`, pick the micro:bit, then `tobbie connect --mac <addr>`")
			}
			return withRobot(cmd.Context(), func(tobbie.Robot) error {
				cfg := tobbie.Config{MAC: addr}
				if err := cfg.Save(); err != nil {
					return err
				}
				fmt.Printf("connected to %s (saved)\n", addr)
				return nil
			})
		},
	}
}

// --- status -------------------------------------------------------

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the saved robot address and whether a link can be opened",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagSim {
				fmt.Println("sim mode")
				return nil
			}
			cfg, err := tobbie.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.MAC == "" {
				fmt.Println("no robot configured (run `tobbie scan` + `tobbie connect`)")
				return nil
			}
			fmt.Printf("configured robot: %s\n", cfg.MAC)
			err = withRobot(cmd.Context(), func(tobbie.Robot) error { return nil })
			if err != nil {
				fmt.Printf("not reachable: %v\n", err)
				return nil
			}
			fmt.Println("reachable: yes")
			return nil
		},
	}
}

// --- move ---------------------------------------------------------

func newMoveCmd() *cobra.Command {
	dirs := make([]string, 0, len(tobbie.AllDirections))
	for _, d := range tobbie.AllDirections {
		dirs = append(dirs, string(d))
	}
	var steps int
	var duration time.Duration
	cmd := &cobra.Command{
		Use:   "move <direction>",
		Short: "Send a movement command",
		Long: fmt.Sprintf(`Send a movement command. Directions: %s.

By default the robot keeps moving until a stop command. Use --steps
(e.g. "move forward --steps 1" for one step, or 5 for five) or
--duration (e.g. "move left --duration 300ms" for a short turn) to
move for a limited time and stop automatically. Calibrated on the
robot: --duration 1500ms = a 180° turn in place (90° ≈ 750ms), scale
proportionally for other angles.`, strings.Join(dirs, ", ")),
		Args:      cobra.ExactArgs(1),
		ValidArgs: dirs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := tobbie.ParseDirection(args[0])
			if err != nil {
				return err
			}
			if steps < 0 {
				return fmt.Errorf("--steps must be >= 0, got %d", steps)
			}
			if duration < 0 {
				return fmt.Errorf("--duration must be >= 0, got %s", duration)
			}
			return withRobot(cmd.Context(), func(r tobbie.Robot) error {
				switch {
				case d == tobbie.DirStop:
					if err := r.Move(d); err != nil {
						return err
					}
					fmt.Println("move: stop")
				case duration > 0:
					if err := r.MoveFor(cmd.Context(), d, duration); err != nil {
						return err
					}
					fmt.Printf("move: %s for %s, then stop\n", d, duration)
				case steps > 0:
					if err := r.Step(cmd.Context(), d, steps); err != nil {
						return err
					}
					fmt.Printf("move: %s (%d steps)\n", d, steps)
				default:
					if err := r.Move(d); err != nil {
						return err
					}
					fmt.Printf("move: %s\n", d)
				}
				if flagSim {
					fmt.Printf("mock wire: %q\n", mockTrace(r))
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&steps, "steps", 0, "move for N discrete steps (each ~625ms, see TOBBIE_STEP_MS), then stop; 0 keeps moving until a stop command")
	cmd.Flags().DurationVar(&duration, "duration", 0, "hold the movement for this long, then stop (e.g. --duration 300ms); takes precedence over --steps")
	return cmd
}

// --- face ---------------------------------------------------------

func newFaceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "face <1-9>",
		Short: "Show one of the nine built-in faces",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := tobbie.ParseFace(args[0])
			if err != nil {
				return err
			}
			return withRobot(cmd.Context(), func(r tobbie.Robot) error {
				if err := r.SetFace(f); err != nil {
					return err
				}
				if flagSim {
					fmt.Printf("mock: face %d -> %q\n", f, mockTrace(r))
				} else {
					fmt.Printf("face: %d\n", f)
				}
				return nil
			})
		},
	}
}

// --- face-set -----------------------------------------------------

func newFaceSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "face-set <grid>",
		Short: "Draw a custom face on the LED matrix",
		Long: `Draw a custom face on the 5x5 LED matrix.

The grid can be given in two forms:

  grid   - five rows of five characters, rows separated by '|', '/' or a
           newline; '#' lights a LED, '.' leaves it off. Example:
             tobbie face-set '#...#|#...#|#####|#...#|#...#'
  rows   - five decimal numbers 0..31, one per row, where each row is a
           bitmask of its LEDs (bit 0 = leftmost). Example:
             tobbie face-set 17 17 31 17 17

Both forms send the same wire command: "C" + five zero-padded row
values (11 chars), which the firmware renders as a face.`,
		Args: cobra.RangeArgs(1, 5),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := tobbie.ParseFaceGrid(strings.Join(args, " "))
			if err != nil {
				return err
			}
			return withRobot(cmd.Context(), func(r tobbie.Robot) error {
				if err := r.DrawFace(rows); err != nil {
					return err
				}
				if flagSim {
					fmt.Printf("mock: face-set %v -> %q\n", rows, mockTrace(r))
				} else {
					fmt.Printf("face-set: %v\n", rows)
				}
				return nil
			})
		},
	}
}

// --- text ---------------------------------------------------------

func newTextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "text <message>",
		Short: "Scroll a text message across the LED matrix",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := args[0]
			return withRobot(cmd.Context(), func(r tobbie.Robot) error {
				if err := r.ShowText(msg); err != nil {
					return err
				}
				if flagSim {
					fmt.Printf("mock: text %q -> %q\n", msg, mockTrace(r))
				} else {
					fmt.Printf("text: %s\n", msg)
				}
				return nil
			})
		},
	}
}

// --- stop-sign ----------------------------------------------------

func newStopSignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop-sign",
		Short: "Show the stop pictogram on the LED matrix",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRobot(cmd.Context(), func(r tobbie.Robot) error {
				if err := r.ShowStopSign(); err != nil {
					return err
				}
				if flagSim {
					fmt.Printf("mock: stop-sign -> %q\n", mockTrace(r))
				} else {
					fmt.Println("stop-sign shown")
				}
				return nil
			})
		},
	}
}

// --- disconnect ---------------------------------------------------

func newDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Close the BLE link (the CLI already disconnects after every command)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagSim {
				fmt.Println("sim mode: nothing to disconnect")
				return nil
			}
			cfg, err := tobbie.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.MAC == "" {
				return fmt.Errorf("no robot configured")
			}
			return withRobot(cmd.Context(), func(tobbie.Robot) error { return nil })
		},
	}
}

func mockTrace(r tobbie.Robot) string {
	if m, ok := r.(*tobbie.Mock); ok {
		cmds := make([]string, 0, len(m.Commands))
		for _, c := range m.Commands {
			cmds = append(cmds, string(c))
		}
		return strings.Join(cmds, ", ")
	}
	return ""
}
