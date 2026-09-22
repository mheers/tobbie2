package judge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/mheers/tobbie2/internal/tobbie"
)

// Gates and caps for the movement resolver. The gates sit in clean
// gaps measured with tools/nl-eval: correct resolutions scored 0.99
// mean confidence, failures 0.39..0.72, and bounded requests scored
// 0.72..0.99 against latched requests at 0.05..0.31.
const (
	kindGate    = 0.70
	sideGate    = 0.70
	boundedGate = 0.60
	maxSteps    = 8
	maxPulse    = 3 * time.Second
)

// MoveQuestions is the validated question set: five typed judgements
// over {"request": <utterance>}. The movement and side choices
// decompose the robot's nine directions the way the firmware composes
// them (forward/backward/pivot combined with left/right), which the
// evaluation showed is the only decomposition without an arc-versus-
// straight ambiguity.
//
// Callers must not mutate the returned map.
func MoveQuestions() map[string]Question {
	return map[string]Question{
		"movement": {
			Type: "choice",
			Instructions: map[string]any{
				"question": "How does the robot move in `request`?",
				"focus":    "Classify the motion the robot performs; the side it curves or turns toward is a separate question. Ignore how far or how long.",
				"language": "`request` may be written in German or English.",
			},
			Criteria: map[string]any{
				"forward":  "walks ahead in the direction it is facing",
				"backward": "walks in reverse, opposite the direction it is facing",
				"pivot":    "turns its body in place without travelling anywhere",
				"stop":     "stops moving and stays in place",
				"none":     "not a movement request for this robot: a question, another robot function, or a movement it does not have such as jumping or strafing sideways",
			},
		},
		"side": {
			Type: "choice",
			Instructions: "Which way does the movement in `request` go? A walking movement curves toward this side; " +
				"`either` means no side is named or none is needed.",
			Criteria: map[string]any{
				"left":   "to the robot's left: turning left, or walking while curving left",
				"right":  "to the robot's right: turning right, or walking while curving right",
				"either": "the request names no side and either side would do (turning around, spinning), or the movement has no side at all (walking straight, stopping)",
			},
		},
		"bounded": {
			Type: "noul",
			Instructions: "Does `request` ask for a bounded amount of movement, rather than telling the robot to keep " +
				"moving until it is stopped?",
			Criteria: map[string]any{
				"true": "says how much: one step, three steps, a little, a bit, briefly, a short distance, 90 degrees, " +
					"half a turn, turn around, do a 180, spin around, all the way around",
				"false": "no amount given: go forward, walk, turn left, keep going; the robot would keep moving until a stop command",
			},
		},
		"walk_amount": {
			Type: "choice",
			Instructions: "How far should the robot walk? Read this only when the robot walks; ignore for turns in place " +
				"and stops.",
			Criteria: map[string]any{
				"1": "exactly one step",
				"2": "two steps",
				"3": "three steps",
				"5": "five steps",
				"8": "eight or more steps; as far as makes sense in one command",
			},
		},
		"turn_amount": {
			Type: "choice",
			Instructions: "How far should the robot turn? Read this only when the robot turns in place; ignore for walking " +
				"and stops.",
			Criteria: map[string]any{
				"45":  "a small adjustment, roughly 45 degrees or less",
				"90":  "about a quarter turn, roughly 90 degrees",
				"180": "about a half turn, roughly 180 degrees",
				"360": "a full turn, all the way around, roughly 360 degrees or more",
			},
		},
	}
}

// MoveResolution is the outcome of interpreting one utterance. The raw
// judgements stay visible for logging and for callers that want to
// compose their own policy.
type MoveResolution struct {
	// Intent is the resolved movement. When it is not Executable the
	// request must not be sent to the robot; Reason says why.
	Intent tobbie.MoveIntent
	// Reason describes a refusal; empty when Intent is executable.
	Reason string

	Kind    Answer // movement question
	Side    Answer // side question
	Bounded Answer // bounded question
}

// Confidence reports the least certain judgement the resolution
// actually consumed: one wrong argument is enough to spoil the call.
// Noul answers carry no confidence, so the bounded question is not
// part of it.
func (r MoveResolution) Confidence() float64 {
	conf := r.Kind.Confidence
	if r.Kind.Choice == "stop" || r.Kind.Choice == "none" {
		return conf
	}
	if r.Side.Confidence < conf {
		conf = r.Side.Confidence
	}
	return conf
}

// InterpretMove resolves a natural-language movement request into a
// MoveIntent. It performs one TypeSafe request and never touches the
// robot.
func (c *Client) InterpretMove(ctx context.Context, request string) (MoveResolution, error) {
	answers, err := c.ask(ctx, map[string]any{"request": request}, MoveQuestions())
	if err != nil {
		return MoveResolution{}, err
	}
	return ResolveMove(answers), nil
}

// ResolveMove maps the question answers onto a MoveIntent. It is pure:
// all calibration, clamping and refusal policy lives here.
func ResolveMove(a Answers) MoveResolution {
	res := MoveResolution{Kind: a["movement"], Side: a["side"], Bounded: a["bounded"]}

	switch {
	case res.Kind.Choice == "none":
		res.Reason = "the request is not a movement this robot can perform"
		return res
	case res.Kind.Confidence < kindGate:
		res.Reason = fmt.Sprintf("the movement is unclear (%s)", candidates(res.Kind))
		return res
	case res.Kind.Choice == "stop":
		res.Intent = tobbie.MoveIntent{Direction: tobbie.DirStop, Mode: tobbie.MoveLatched}
		return res
	}

	switch res.Side.Choice {
	case "left", "right":
		if res.Side.Confidence < sideGate {
			res.Reason = fmt.Sprintf("the side is unclear (%s)", candidates(res.Side))
			return res
		}
	case "either":
		// fine: either side satisfies the request
	default:
		res.Reason = "the side did not resolve"
		return res
	}

	dir := directionFor(res.Kind.Choice, res.Side.Choice)
	if dir == "" {
		res.Reason = fmt.Sprintf("the movement %q did not resolve to a direction", res.Kind.Choice)
		return res
	}

	if res.Bounded.Noul < boundedGate {
		res.Intent = tobbie.MoveIntent{Direction: dir, Mode: tobbie.MoveLatched}
		return res
	}

	if isPivot(dir) {
		deg, err := strconv.Atoi(a["turn_amount"].Choice)
		if err != nil {
			res.Reason = "the turn amount did not resolve"
			return res
		}
		dur := time.Duration(float64(deg) / 180.0 * float64(tobbie.TurnCalibration180))
		if dur > maxPulse {
			dur = maxPulse
		}
		res.Intent = tobbie.MoveIntent{Direction: dir, Mode: tobbie.MoveTimed, Duration: dur}
		return res
	}

	steps, err := strconv.Atoi(a["walk_amount"].Choice)
	if err != nil {
		res.Reason = "the walk amount did not resolve"
		return res
	}
	if steps > maxSteps {
		steps = maxSteps
	}
	res.Intent = tobbie.MoveIntent{Direction: dir, Mode: tobbie.MoveSteps, Steps: steps}
	return res
}

// directionFor composes the travel direction and the side into one of
// the nine wire directions. A pivot or an arc with side "either"
// defaults to the robot's right; for those requests only the amount
// matters.
func directionFor(movement, side string) tobbie.Direction {
	left := side == "left"
	switch movement {
	case "forward":
		if side == "either" {
			return tobbie.DirForward
		}
		if left {
			return tobbie.DirForwardLeft
		}
		return tobbie.DirForwardRight
	case "backward":
		if side == "either" {
			return tobbie.DirBackward
		}
		if left {
			return tobbie.DirBackwardLeft
		}
		return tobbie.DirBackwardRight
	case "pivot":
		if left {
			return tobbie.DirLeft
		}
		return tobbie.DirRight
	case "stop":
		return tobbie.DirStop
	}
	return ""
}

func isPivot(d tobbie.Direction) bool {
	return d == tobbie.DirLeft || d == tobbie.DirRight
}

// candidates renders the most likely options of a choice answer, for a
// refusal message that tells the caller what to disambiguate.
func candidates(a Answer) string {
	type option struct {
		name string
		p    float64
	}
	opts := make([]option, 0, len(a.Probabilities))
	for name, p := range a.Probabilities {
		if p >= 0.1 {
			opts = append(opts, option{name, p})
		}
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].p > opts[j].p })
	if len(opts) == 0 {
		return "no clear option"
	}
	out := ""
	for i, o := range opts {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s %.2f", o.name, o.p)
	}
	return out
}
