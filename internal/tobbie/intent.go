package tobbie

import (
	"context"
	"fmt"
	"time"
)

// MoveMode is how a resolved movement ends.
type MoveMode int

const (
	// MoveNone means the request did not resolve to an executable
	// movement.
	MoveNone MoveMode = iota
	// MoveLatched moves until a stop command arrives (the firmware's
	// native behaviour).
	MoveLatched
	// MoveSteps moves for a bounded number of discrete steps and
	// stops.
	MoveSteps
	// MoveTimed holds the movement for a duration and stops.
	MoveTimed
)

func (m MoveMode) String() string {
	switch m {
	case MoveLatched:
		return "latched"
	case MoveSteps:
		return "steps"
	case MoveTimed:
		return "timed"
	}
	return "none"
}

// MoveIntent is a movement that has been resolved to concrete robot
// arguments: a direction plus how the movement ends. internal/judge
// produces one from a natural-language request; Execute runs it
// against a Robot.
type MoveIntent struct {
	Direction Direction
	Mode      MoveMode
	Steps     int           // MoveSteps only
	Duration  time.Duration // MoveTimed only
}

// Executable reports whether the intent is a movement that can be sent
// to the robot.
func (m MoveIntent) Executable() bool {
	return m.Mode != MoveNone && m.Direction != ""
}

// Execute runs the intent against r. Steps and timed movements stop on
// their own; a latched movement keeps going until a stop command.
func (m MoveIntent) Execute(ctx context.Context, r Robot) error {
	switch m.Mode {
	case MoveLatched:
		return r.Move(m.Direction)
	case MoveSteps:
		return r.Step(ctx, m.Direction, m.Steps)
	case MoveTimed:
		return r.MoveFor(ctx, m.Direction, m.Duration)
	}
	return fmt.Errorf("tobbie: no movement to execute")
}

func (m MoveIntent) String() string {
	switch m.Mode {
	case MoveLatched:
		return fmt.Sprintf("%s (latched)", m.Direction)
	case MoveSteps:
		return fmt.Sprintf("%s (%d steps)", m.Direction, m.Steps)
	case MoveTimed:
		return fmt.Sprintf("%s (%s)", m.Direction, m.Duration)
	}
	return "no movement"
}
