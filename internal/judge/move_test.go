package judge

import (
	"strings"
	"testing"
	"time"

	"github.com/mheers/tobbie2/internal/tobbie"
)

func choice(name string, conf float64, probs map[string]float64) Answer {
	return Answer{Type: "choice", Choice: name, Confidence: conf, Probabilities: probs}
}

func noul(v float64) Answer { return Answer{Type: "noul", Noul: v} }

// movement is the common prefix: a confident kind answer plus the
// answers every movement needs.
func movement(kind string, conf float64, side string, sideConf float64, bounded float64) Answers {
	return Answers{
		"movement": choice(kind, conf, map[string]float64{kind: conf}),
		"side":     choice(side, sideConf, map[string]float64{side: sideConf}),
		"bounded":  noul(bounded),
	}
}

func TestResolveMove(t *testing.T) {
	tests := []struct {
		name    string
		answers Answers
		want    tobbie.MoveIntent
		refuse  string // substring expected in Reason; empty means executable
	}{
		{
			name:    "latched forward",
			answers: movement("forward", 1, "either", 1, 0.05),
			want:    tobbie.MoveIntent{Direction: tobbie.DirForward, Mode: tobbie.MoveLatched},
		},
		{
			name: "three steps",
			answers: func() Answers {
				a := movement("forward", 0.99, "either", 1, 0.98)
				a["walk_amount"] = choice("3", 0.95, map[string]float64{"3": 0.95})
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirForward, Mode: tobbie.MoveSteps, Steps: 3},
		},
		{
			name: "walking with a side becomes an arc",
			answers: func() Answers {
				a := movement("forward", 1, "left", 0.98, 0.08)
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirForwardLeft, Mode: tobbie.MoveLatched},
		},
		{
			name: "backwards with a side becomes an arc",
			answers: func() Answers {
				return movement("backward", 1, "right", 0.97, 0.09)
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirBackwardRight, Mode: tobbie.MoveLatched},
		},
		{
			name: "u-turn with either side defaults to right and is timed",
			answers: func() Answers {
				a := movement("pivot", 0.96, "either", 1, 0.95)
				a["turn_amount"] = choice("180", 0.94, map[string]float64{"180": 0.94})
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirRight, Mode: tobbie.MoveTimed, Duration: 1500 * time.Millisecond},
		},
		{
			name: "quarter turn left",
			answers: func() Answers {
				a := movement("pivot", 1, "left", 0.99, 0.98)
				a["turn_amount"] = choice("90", 0.97, map[string]float64{"90": 0.97})
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirLeft, Mode: tobbie.MoveTimed, Duration: 750 * time.Millisecond},
		},
		{
			name: "full turn is capped at the pulse maximum",
			answers: func() Answers {
				a := movement("pivot", 0.98, "left", 0.9, 0.97)
				a["turn_amount"] = choice("360", 0.93, map[string]float64{"360": 0.93})
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirLeft, Mode: tobbie.MoveTimed, Duration: maxPulse},
		},
		{
			name: "stop ignores side and amount",
			answers: Answers{
				"movement": choice("stop", 0.99, map[string]float64{"stop": 0.99}),
				"side":     choice("either", 0.9, map[string]float64{"either": 0.9}),
				"bounded":  noul(0.8),
			},
			want: tobbie.MoveIntent{Direction: tobbie.DirStop, Mode: tobbie.MoveLatched},
		},
		{
			name: "not a movement request is refused",
			answers: Answers{
				"movement": choice("none", 0.99, map[string]float64{"none": 0.99}),
			},
			refuse: "not a movement",
		},
		{
			name: "low kind confidence is refused, not guessed",
			answers: Answers{
				"movement": choice("backward", 0.46, map[string]float64{"backward": 0.51, "pivot": 0.46}),
			},
			refuse: "unclear",
		},
		{
			name:    "low side confidence is refused",
			answers: movement("pivot", 0.97, "right", 0.47, 0.99),
			refuse:  "side is unclear",
		},
		{
			name: "walk amount above the cap is clamped",
			answers: func() Answers {
				a := movement("forward", 1, "either", 1, 0.95)
				a["walk_amount"] = choice("12", 0.9, map[string]float64{"12": 0.9})
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirForward, Mode: tobbie.MoveSteps, Steps: maxSteps},
		},
		{
			name: "turn above the cap is clamped",
			answers: func() Answers {
				a := movement("pivot", 1, "right", 0.95, 0.95)
				a["turn_amount"] = choice("720", 0.9, map[string]float64{"720": 0.9})
				return a
			}(),
			want: tobbie.MoveIntent{Direction: tobbie.DirRight, Mode: tobbie.MoveTimed, Duration: maxPulse},
		},
		{
			name: "unexpected walk amount is refused",
			answers: func() Answers {
				a := movement("forward", 1, "either", 1, 0.95)
				a["walk_amount"] = choice("many", 0.9, map[string]float64{"many": 0.9})
				return a
			}(),
			refuse: "walk amount",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMove(tt.answers)
			if tt.refuse != "" {
				if got.Intent.Executable() {
					t.Fatalf("resolved to %v, want refusal containing %q", got.Intent, tt.refuse)
				}
				if !strings.Contains(got.Reason, tt.refuse) {
					t.Fatalf("Reason = %q, want it to contain %q", got.Reason, tt.refuse)
				}
				return
			}
			if got.Reason != "" {
				t.Fatalf("unexpected refusal: %s", got.Reason)
			}
			if got.Intent != tt.want {
				t.Fatalf("Intent = %+v, want %+v", got.Intent, tt.want)
			}
		})
	}
}

func TestMoveResolutionConfidence(t *testing.T) {
	// The least certain consumed judgement wins.
	res := ResolveMove(Answers{
		"movement":    choice("pivot", 0.90, nil),
		"side":        choice("left", 0.80, nil),
		"bounded":     noul(0.9),
		"turn_amount": choice("90", 0.95, nil),
	})
	if got := res.Confidence(); got != 0.80 {
		t.Errorf("Confidence() = %.2f, want 0.80", got)
	}

	// A stop consumes only the kind judgement.
	res = ResolveMove(Answers{
		"movement": choice("stop", 0.99, nil),
		"side":     choice("either", 0.10, nil),
	})
	if got := res.Confidence(); got != 0.99 {
		t.Errorf("Confidence() = %.2f, want 0.99", got)
	}
}
