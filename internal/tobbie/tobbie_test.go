package tobbie

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestWireMovementCommands(t *testing.T) {
	want := map[Direction]string{
		DirForwardLeft:   "A0\n",
		DirForward:       "A1\n",
		DirForwardRight:  "A2\n",
		DirLeft:          "A3\n",
		DirStop:          "A4\n",
		DirRight:         "A5\n",
		DirBackwardRight: "A6\n",
		DirBackward:      "A7\n",
		DirBackwardLeft:  "A8\n",
	}
	for d, w := range want {
		if got := string(encMove(d)); got != w {
			t.Errorf("encMove(%s) = %q, want %q", d, got, w)
		}
	}
}

func TestWireFaceCommands(t *testing.T) {
	want := map[Face]string{
		Face1: "C01\n",
		Face5: "C05\n",
		Face9: "C09\n",
	}
	for f, w := range want {
		if got := string(encFace(f)); got != w {
			t.Errorf("encFace(%d) = %q, want %q", f, got, w)
		}
	}
}

func TestWireFaceSet(t *testing.T) {
	// "O" letter: rows 10001, 10001, 11111, 10001, 10001.
	rows := [5]byte{17, 17, 31, 17, 17}
	got := string(encFaceSet(rows))
	want := "C1717311717\n"
	if got != want {
		t.Errorf("encFaceSet = %q, want %q", got, want)
	}
	if len(got) != 12 { // "C" + 10 digits + "\n"
		t.Errorf("encFaceSet length = %d, want 12", len(got))
	}
}

func TestWireTextAndStopSign(t *testing.T) {
	if got := string(encText("hello")); got != "Zhello\n" {
		t.Errorf("encText = %q", got)
	}
	if got := string(encStopSign()); got != "B00\n" {
		t.Errorf("encStopSign = %q", got)
	}
}

func TestParseDirection(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Direction
	}{
		{"forward", DirForward},
		{"FORWARD", DirForward},
		{"forward-right", DirForwardRight},
		{"a4", DirStop},
		{"halt", DirStop},
		{"backward-left", DirBackwardLeft},
	} {
		got, err := ParseDirection(tc.in)
		if err != nil {
			t.Fatalf("ParseDirection(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseDirection(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	if _, err := ParseDirection("diagonal"); err == nil {
		t.Error("ParseDirection(diagonal) should fail")
	}
}

func TestParseFace(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Face
	}{
		{"1", Face1},
		{"9", Face9},
	} {
		got, err := ParseFace(tc.in)
		if err != nil {
			t.Fatalf("ParseFace(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseFace(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"0", "10", "x"} {
		if _, err := ParseFace(bad); err == nil {
			t.Errorf("ParseFace(%q) should fail", bad)
		}
	}
}

func TestParseFaceGrid(t *testing.T) {
	rows, err := ParseFaceGrid("#...#|#...#|#####|#...#|#...#")
	if err != nil {
		t.Fatal(err)
	}
	want := [5]byte{17, 17, 31, 17, 17}
	if rows != want {
		t.Errorf("grid parse = %v, want %v", rows, want)
	}

	rows, err = ParseFaceGrid("17 17 31 17 17")
	if err != nil {
		t.Fatal(err)
	}
	if rows != want {
		t.Errorf("numeric parse = %v, want %v", rows, want)
	}

	if _, err := ParseFaceGrid("###"); err == nil {
		t.Error("short grid should fail")
	}
	if _, err := ParseFaceGrid("#....|#....|#....|#....|...."); err == nil {
		t.Error("4-cell row should fail")
	}
	if _, err := ParseFaceGrid("99 0 0 0 0"); err == nil {
		t.Error("row value > 31 should fail")
	}
	if _, err := ParseFaceGrid("1 2 3 4"); err == nil {
		t.Error("4 rows should fail")
	}
}

func TestMockRecordsWireCommands(t *testing.T) {
	m := NewMock()
	ctx := context.Background()

	if err := m.Move(DirForward); err != ErrNotConnected {
		t.Fatalf("Move before Connect = %v, want ErrNotConnected", err)
	}
	if err := m.Connect(ctx, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}
	if !m.Connected() {
		t.Fatal("mock should report connected")
	}

	_ = m.Move(DirForward)
	_ = m.Move(DirStop)
	_ = m.SetFace(Face3)
	_ = m.DrawFace([5]byte{17, 17, 31, 17, 17})
	_ = m.ShowText("hi")
	_ = m.ShowStopSign()

	want := []string{"A1\n", "A4\n", "C03\n", "C1717311717\n", "Zhi\n", "B00\n"}
	if len(m.Commands) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%s", len(m.Commands), len(want), m.String())
	}
	for i, w := range want {
		if !bytes.Equal(m.Commands[i], []byte(w)) {
			t.Errorf("command[%d] = %q, want %q", i, m.Commands[i], w)
		}
	}

	if m.Last.Direction != DirStop {
		t.Errorf("Last.Direction = %s, want stop", m.Last.Direction)
	}
	if m.Last.Face != Face3 {
		t.Errorf("Last.Face = %d, want 3", m.Last.Face)
	}
	if m.Last.Rows != [5]byte{17, 17, 31, 17, 17} {
		t.Errorf("Last.Rows = %v", m.Last.Rows)
	}
	if m.Last.Text != "hi" || !m.Last.StopSign {
		t.Errorf("Last = %+v", m.Last)
	}

	if err := m.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m.Connected() {
		t.Fatal("mock should report disconnected")
	}
	if err := m.Move(DirForward); err != ErrNotConnected {
		t.Fatalf("Move after Disconnect = %v, want ErrNotConnected", err)
	}
}

func TestStepMovesForNStepsThenStops(t *testing.T) {
	old := StepDuration
	StepDuration = time.Millisecond
	defer func() { StepDuration = old }()

	m := NewMock()
	ctx := context.Background()
	if err := m.Connect(ctx, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}

	if err := m.Step(ctx, DirForward, 3); err != nil {
		t.Fatal(err)
	}
	if err := m.Step(ctx, DirLeft, 1); err != nil {
		t.Fatal(err)
	}

	want := []string{"A1\n", "A4\n", "A3\n", "A4\n"}
	if len(m.Commands) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%s", len(m.Commands), len(want), m.String())
	}
	for i, w := range want {
		if !bytes.Equal(m.Commands[i], []byte(w)) {
			t.Errorf("command[%d] = %q, want %q", i, m.Commands[i], w)
		}
	}
}

func TestStepWithNonPositiveStepsJustStops(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	if err := m.Connect(ctx, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}

	for _, n := range []int{0, -1} {
		if err := m.Step(ctx, DirForward, n); err != nil {
			t.Fatalf("Step(_, _, %d): %v", n, err)
		}
	}
	want := []string{"A4\n", "A4\n"}
	if len(m.Commands) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%s", len(m.Commands), len(want), m.String())
	}
	for i, w := range want {
		if !bytes.Equal(m.Commands[i], []byte(w)) {
			t.Errorf("command[%d] = %q, want %q", i, m.Commands[i], w)
		}
	}
}

func TestMoveForHoldsDirectionThenStops(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	if err := m.Connect(ctx, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}

	if err := m.MoveFor(ctx, DirRight, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := []string{"A5\n", "A4\n"}
	if len(m.Commands) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%s", len(m.Commands), len(want), m.String())
	}
	for i, w := range want {
		if !bytes.Equal(m.Commands[i], []byte(w)) {
			t.Errorf("command[%d] = %q, want %q", i, m.Commands[i], w)
		}
	}
}

func TestMoveForStopsEvenWhenCanceled(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	if err := m.Connect(ctx, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.MoveFor(cctx, DirForward, time.Hour); err != nil {
		t.Fatal(err)
	}
	want := []string{"A1\n", "A4\n"}
	if len(m.Commands) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%s", len(m.Commands), len(want), m.String())
	}
	for i, w := range want {
		if !bytes.Equal(m.Commands[i], []byte(w)) {
			t.Errorf("command[%d] = %q, want %q", i, m.Commands[i], w)
		}
	}
}
