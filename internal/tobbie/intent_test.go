package tobbie

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestMoveIntentExecute(t *testing.T) {
	old := StepDuration
	StepDuration = time.Millisecond
	defer func() { StepDuration = old }()

	m := NewMock()
	ctx := context.Background()
	if err := m.Connect(ctx, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		intent MoveIntent
		want   []string
	}{
		{
			name:   "latched",
			intent: MoveIntent{Direction: DirForward, Mode: MoveLatched},
			want:   []string{"A1\n"},
		},
		{
			name:   "steps",
			intent: MoveIntent{Direction: DirBackward, Mode: MoveSteps, Steps: 3},
			want:   []string{"A7\n", "A4\n"},
		},
		{
			name:   "timed",
			intent: MoveIntent{Direction: DirRight, Mode: MoveTimed, Duration: 5 * time.Millisecond},
			want:   []string{"A5\n", "A4\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := len(m.Commands)
			if err := tc.intent.Execute(ctx, m); err != nil {
				t.Fatal(err)
			}
			got := m.Commands[start:]
			if len(got) != len(tc.want) {
				t.Fatalf("sent %d commands, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if !bytes.Equal(got[i], []byte(w)) {
					t.Errorf("command[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}

	if err := (MoveIntent{}).Execute(ctx, m); err == nil {
		t.Error("a zero intent should not execute")
	}
	if (MoveIntent{}).Executable() {
		t.Error("a zero intent should not be executable")
	}
}
