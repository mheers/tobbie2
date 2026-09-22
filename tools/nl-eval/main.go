// Command nl-eval evaluates proposed TypeSafe question sets for
// resolving natural-language movement requests into typed Tobbie II
// robot commands.
//
// One request is sent per utterance; the structured answers are
// resolved into a movement plan by the same code an implementation
// would use; the plan is compared against the expected outcome.
//
// Three designs are compared:
//
//	v1  one Choice over the nine concrete directions (+ none)
//	v2  a Choice over movement *kind* (walk/pivot/arc/stop) plus a
//	    Choice over side (left/right/either), composed in code
//	v3  travel direction (forward/backward/pivot/stop) plus side,
//	    with the arcing directions composed in code — the firmware's
//	    own decomposition of its nine commands. v3 imports the
//	    question set and the resolver from internal/judge, so it
//	    always evaluates exactly what the MCP server ships.
//
//	TYPESAFE_API_KEY=... go run ./tools/nl-eval -design v1
//	TYPESAFE_API_KEY=... go run ./tools/nl-eval -design v3
//
// Flags:
//
//	-design v1|v2|v3   which question set to evaluate (default v3)
//	-set tune|holdout  which utterance set to run (default tune)
//	-limit N        only run the first N utterances (smoke test)
//	-dry            print the request body for the first utterance and exit
//
// Full raw answers are written to /tmp/opencode/nl-eval-<design>-<set>.json.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mheers/tobbie2/internal/judge"
	"github.com/mheers/tobbie2/internal/tobbie"
)

const (
	endpoint = "https://api.typesafe.ai/v1/systemone"
	model    = "jev-latest"

	// Mirrors tobbie.TurnCalibration180 / StepDuration. Calibration is
	// applied in code; the questions only ask for degrees and steps.
	turnCalibration180ms = 1500
	maxSteps             = 8
	maxPulse             = 3 * time.Second

	// Gates used by the resolvers under evaluation.
	kindGate    = 0.70
	sideGate    = 0.70
	boundedGate = 0.60
)

// --- TypeSafe request/response types --------------------------------

// question and answer alias the shipped types so the v3 design below
// exercises the same spec and resolver as the MCP server.
type question = judge.Question
type answer = judge.Answer

type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// boundedQuestion is shared by both designs and is the presence check
// that keeps "walk forward" (latched) apart from "walk forward a bit"
// (bounded).
func boundedQuestion() question {
	return question{
		Type: "noul",
		Instructions: "Does `request` ask for a bounded amount of movement, rather than telling the robot to keep moving " +
			"until it is stopped?",
		Criteria: map[string]any{
			"true": "says how much: one step, three steps, a little, a bit, briefly, a short distance, 90 degrees, " +
				"half a turn, turn around, do a 180, spin around, all the way around",
			"false": "no amount given: go forward, walk, turn left, keep going; the robot would keep moving until a stop command",
		},
	}
}

func walkAmountQuestion() question {
	return question{
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
	}
}

func turnAmountQuestion() question {
	return question{
		Type: "choice",
		Instructions: "How far should the robot turn? Read this only when the robot turns in place; ignore for walking " +
			"and stops.",
		Criteria: map[string]any{
			"45":  "a small adjustment, roughly 45 degrees or less",
			"90":  "about a quarter turn, roughly 90 degrees",
			"180": "about a half turn, roughly 180 degrees",
			"360": "a full turn, all the way around, roughly 360 degrees or more",
		},
	}
}

// --- design v1: one Choice over the nine directions -----------------

func questionsV1() map[string]question {
	return map[string]question{
		"direction": {
			Type: "choice",
			Instructions: map[string]any{
				"question": "Which movement does `request` ask the robot to perform?",
				"focus":    "Classify the physical movement only. Ignore how far or how long.",
				"language": "`request` may be written in German or English.",
			},
			Criteria: map[string]any{
				"forward":        "walks straight ahead, away from its face",
				"backward":       "walks straight backwards, toward its face",
				"left":           "pivots in place to its left; does not walk",
				"right":          "pivots in place to its right; does not walk",
				"forward-left":   "walks forward while curving to its left; not a pivot in place",
				"forward-right":  "walks forward while curving to its right; not a pivot in place",
				"backward-left":  "walks backwards while curving to its left",
				"backward-right": "walks backwards while curving to its right",
				"stop":           "stops moving and stays in place",
				"none":           "not a movement request for this robot: a question, another robot function, or a movement it does not have such as jumping or strafing sideways",
			},
		},
		"bounded":     boundedQuestion(),
		"walk_amount": walkAmountQuestion(),
		"turn_amount": turnAmountQuestion(),
	}
}

func resolveV1(a map[string]answer) plan {
	d := a["direction"]
	if d.Choice == "none" {
		return plan{mode: "none"}
	}
	if d.Confidence < kindGate {
		return plan{mode: "none", escalated: true}
	}
	return planFor(d.Choice, a)
}

// --- design v2: movement kind + side, composed in code --------------

func questionsV2() map[string]question {
	return map[string]question{
		"movement": {
			Type: "choice",
			Instructions: map[string]any{
				"question": "What kind of movement does `request` ask the robot to perform?",
				"focus":    "Classify the kind of movement only; the side it happens on is a separate question. Ignore how far or how long.",
				"language": "`request` may be written in German or English.",
			},
			Criteria: map[string]any{
				"walk_forward":  "walks straight ahead without deliberately changing its heading",
				"walk_backward": "walks straight backwards without deliberately changing its heading",
				"pivot":         "rotates in place without walking",
				"arc_forward":   "walks forward while curving; not a pivot in place",
				"arc_backward":  "walks backwards while curving; not a pivot in place",
				"stop":          "stops moving and stays in place",
				"none":          "not a movement request for this robot: a question, another robot function, or a movement it does not have such as jumping or strafing sideways",
			},
		},
		"side": {
			Type: "choice",
			Instructions: "Which side does the movement in `request` use? Choose `either` when the movement has no side " +
				"to select (walking straight, stopping) or when either side would satisfy the request.",
			Criteria: map[string]any{
				"left":   "the movement goes to the robot's left",
				"right":  "the movement goes to the robot's right",
				"either": "no side is needed or either side works: straight walking, stopping, turn around, spin, do a 180",
			},
		},
		"bounded":     boundedQuestion(),
		"walk_amount": walkAmountQuestion(),
		"turn_amount": turnAmountQuestion(),
	}
}

func needsSide(movement string) bool {
	switch movement {
	case "pivot", "arc_forward", "arc_backward":
		return true
	}
	return false
}

// directionForV2 maps v2's movement kinds to a wire direction. Walking
// kinds stay straight and ignore the side; arcing is its own kind.
func directionForV2(movement, side string) string {
	switch movement {
	case "walk_forward":
		return "forward"
	case "walk_backward":
		return "backward"
	case "pivot":
		if side == "left" {
			return "left"
		}
		return "right"
	case "arc_forward":
		if side == "left" {
			return "forward-left"
		}
		return "forward-right"
	case "arc_backward":
		if side == "left" {
			return "backward-left"
		}
		return "backward-right"
	case "stop":
		return "stop"
	}
	return ""
}

// --- design v3: travel direction + side (shipped) -------------------
//
// v3 is the design wired into internal/judge: the movement question
// only decides where the robot travels or that it pivots, the side
// question carries the curve, and walking with a side becomes an arc
// in code. This is the firmware's own decomposition — forward() and
// backward() combine with leftward()/rightward() — and it leaves no
// arc-versus-straight ambiguity in the movement question.
//
// The question set and the resolver come from the shipped package, so
// this design always evaluates the code that runs in the MCP server.

func questionsV3() map[string]question {
	return judge.MoveQuestions()
}

func resolveV3(a map[string]answer) plan {
	res := judge.ResolveMove(a)
	p := plan{
		dir:      string(res.Intent.Direction),
		mode:     moveModeName(res.Intent.Mode),
		steps:    res.Intent.Steps,
		duration: res.Intent.Duration,
	}
	if !res.Intent.Executable() {
		p.dir = ""
		p.mode = "none"
		p.escalated = res.Reason != "" && res.Kind.Choice != "none"
	}
	return p
}

func moveModeName(m tobbie.MoveMode) string {
	switch m {
	case tobbie.MoveLatched:
		return "latched"
	case tobbie.MoveSteps:
		return "steps"
	case tobbie.MoveTimed:
		return "timed"
	}
	return "none"
}

func sideOK(s answer) bool {
	switch s.Choice {
	case "left", "right":
		return s.Confidence >= sideGate
	case "either":
		return true
	}
	return false
}

func resolveV2(a map[string]answer) plan {
	m := a["movement"]
	if m.Choice == "none" {
		return plan{mode: "none"}
	}
	if m.Confidence < kindGate {
		return plan{mode: "none", escalated: true}
	}
	if needsSide(m.Choice) && !sideOK(a["side"]) {
		return plan{mode: "none", escalated: true}
	}
	dir := directionForV2(m.Choice, a["side"].Choice)
	if dir == "" {
		return plan{mode: "none", escalated: true}
	}
	return planFor(dir, a)
}

// --- shared plan logic ----------------------------------------------

type plan struct {
	dir       string
	mode      string // latched | steps | timed | none
	steps     int
	duration  time.Duration
	escalated bool
}

func isPivot(dir string) bool { return dir == "left" || dir == "right" }

// planFor turns a concrete direction plus the amount answers into a
// movement plan. All calibration lives here, never in the prompt.
func planFor(dir string, a map[string]answer) plan {
	if dir == "stop" {
		return plan{dir: "stop", mode: "latched"}
	}
	if a["bounded"].Noul < boundedGate {
		return plan{dir: dir, mode: "latched"}
	}
	if isPivot(dir) {
		deg, err := strconv.Atoi(a["turn_amount"].Choice)
		if err != nil {
			return plan{dir: dir, mode: "latched"}
		}
		dur := time.Duration(float64(deg) / 180.0 * turnCalibration180ms)
		if dur > maxPulse {
			dur = maxPulse
		}
		return plan{dir: dir, mode: "timed", duration: dur * time.Millisecond}
	}
	steps, err := strconv.Atoi(a["walk_amount"].Choice)
	if err != nil {
		return plan{dir: dir, mode: "latched"}
	}
	if steps > maxSteps {
		steps = maxSteps
	}
	return plan{dir: dir, mode: "steps", steps: steps}
}

func (p plan) String() string {
	switch p.mode {
	case "none":
		if p.escalated {
			return "declined(low conf)"
		}
		return "declined"
	case "latched":
		return p.dir + " latched"
	case "steps":
		return fmt.Sprintf("%s %d steps", p.dir, p.steps)
	case "timed":
		return fmt.Sprintf("%s %dms", p.dir, p.duration/time.Millisecond)
	}
	return "?"
}

// --- expectations ---------------------------------------------------

type exp struct {
	Dirs []string `json:"dirs,omitempty"` // acceptable resolved directions
	Mode string   `json:"mode"`           // latched | steps | timed | none
	Min  float64  `json:"min,omitempty"`  // steps or milliseconds, depending on mode
	Max  float64  `json:"max,omitempty"`
	Note string   `json:"note,omitempty"`
}

var items = []struct {
	utterance string
	exp       exp
}{
	{"forward", exp{Dirs: []string{"forward"}, Mode: "latched"}},
	{"geh einen Schritt", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 1, Max: 1}},
	{"geh drei Schritte vorwärts", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 3, Max: 3}},
	{"go forward five steps", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 5, Max: 5}},
	{"move forward", exp{Dirs: []string{"forward"}, Mode: "latched"}},
	{"vorwärts", exp{Dirs: []string{"forward"}, Mode: "latched"}},
	{"geh geradeaus", exp{Dirs: []string{"forward"}, Mode: "latched"}},
	{"rückwärts", exp{Dirs: []string{"backward"}, Mode: "latched"}},
	{"walk backwards to the right", exp{Dirs: []string{"backward-right"}, Mode: "latched"}},
	{"go forward and curve left", exp{Dirs: []string{"forward-left"}, Mode: "latched"}},
	{"go forward and curve right", exp{Dirs: []string{"forward-right"}, Mode: "latched"}},
	{"geh rückwärts und links", exp{Dirs: []string{"backward-left"}, Mode: "latched"}},
	{"stop", exp{Dirs: []string{"stop"}, Mode: "latched"}},
	{"halt", exp{Dirs: []string{"stop"}, Mode: "latched"}},
	{"bleib stehen", exp{Dirs: []string{"stop"}, Mode: "latched"}},
	{"stop right now", exp{Dirs: []string{"stop"}, Mode: "latched"}},
	{"dreh dich ein bisschen nach links", exp{Dirs: []string{"left"}, Mode: "timed", Min: 200, Max: 700}},
	{"turn left a little", exp{Dirs: []string{"left"}, Mode: "timed", Min: 200, Max: 700}},
	{"ein kleines Stück nach vorne", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 1, Max: 3}},
	{"turn 90 degrees to the right", exp{Dirs: []string{"right"}, Mode: "timed", Min: 600, Max: 1000}},
	{"dreh dich nach links um 45 grad", exp{Dirs: []string{"left"}, Mode: "timed", Min: 250, Max: 600}},
	{"turn around", exp{Dirs: []string{"left", "right"}, Mode: "timed", Min: 1100, Max: 2000}},
	{"mach eine Kehrtwende", exp{Dirs: []string{"left", "right"}, Mode: "timed", Min: 1100, Max: 2000}},
	{"do a 180", exp{Dirs: []string{"left", "right"}, Mode: "timed", Min: 1100, Max: 2000}},
	{"spin around", exp{Dirs: []string{"left", "right"}, Mode: "timed", Min: 2400, Max: 4000}},
	{"turn left all the way around", exp{Dirs: []string{"left"}, Mode: "timed", Min: 2400, Max: 4000}},
	{"langsam vorwärts", exp{Dirs: []string{"forward"}, Mode: "latched", Note: "speed is not selectable"}},
	{"geh weiter", exp{Dirs: []string{"forward"}, Mode: "latched"}},
	{"komm zurück", exp{Dirs: []string{"backward"}, Mode: "latched", Note: "ambiguous: from the robot's frame, coming back is backwards"}},
	{"dreh dich nach rechts", exp{Dirs: []string{"right"}, Mode: "latched", Note: "unbounded pivot: keeps turning until stopped"}},
	{"geh ein paar Schritte rückwärts", exp{Dirs: []string{"backward"}, Mode: "steps", Min: 2, Max: 4}},
	{"go backwards a little", exp{Dirs: []string{"backward"}, Mode: "steps", Min: 1, Max: 4}},
	{"walk forward", exp{Dirs: []string{"forward"}, Mode: "latched"}},
	{"turn right 180 degrees", exp{Dirs: []string{"right"}, Mode: "timed", Min: 1100, Max: 2000}},
	{"vorwärts, aber nur ein kleines bisschen", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 1, Max: 3}},
	{"dance", exp{Mode: "none"}},
	{"what's your name?", exp{Mode: "none"}},
	{"jump", exp{Mode: "none"}},
	{"zeig mir ein lächelndes Gesicht", exp{Mode: "none", Note: "face request: the move resolver must decline"}},
	{"how far can you walk?", exp{Mode: "none"}},
}

// holdoutItems were written after the tune set and are not used for
// prompt tuning; they check that the fixes generalize.
var holdoutItems = []struct {
	utterance string
	exp       exp
}{
	{"mach bitte kehrt", exp{Dirs: []string{"left", "right"}, Mode: "timed", Min: 1100, Max: 2000, Note: "German U-turn"}},
	{"dreh dich einmal um dich selbst", exp{Dirs: []string{"left", "right"}, Mode: "timed", Min: 2400, Max: 4000}},
	{"fahre rückwärts", exp{Dirs: []string{"backward"}, Mode: "latched"}},
	{"geh nach hinten", exp{Dirs: []string{"backward"}, Mode: "latched", Note: "hinten = behind the robot"}},
	{"walk forward for a bit", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 1, Max: 3}},
	{"come forward slowly", exp{Dirs: []string{"forward"}, Mode: "latched", Note: "speed is not selectable"}},
	{"reverse for two steps", exp{Dirs: []string{"backward"}, Mode: "steps", Min: 2, Max: 2}},
	{"turn left 360", exp{Dirs: []string{"left"}, Mode: "timed", Min: 2400, Max: 4000}},
	{"bitte anhalten", exp{Dirs: []string{"stop"}, Mode: "latched"}},
	{"10 Schritte vorwärts", exp{Dirs: []string{"forward"}, Mode: "steps", Min: 8, Max: 8, Note: "clamped by maxSteps"}},
	{"what can you do?", exp{Mode: "none"}},
	{"los", exp{Dirs: []string{"forward"}, Mode: "latched", Note: "los = go"}},
	{"ein Stückchen rückwärts", exp{Dirs: []string{"backward"}, Mode: "steps", Min: 1, Max: 3}},
}

func eval(p plan, e exp) (ok bool, why string) {
	if e.Mode == "none" {
		if p.mode == "none" {
			return true, ""
		}
		return false, "should decline"
	}
	if p.escalated {
		return false, "escalated"
	}
	found := false
	for _, d := range e.Dirs {
		if p.dir == d {
			found = true
			break
		}
	}
	if !found {
		return false, "direction"
	}
	if p.mode != e.Mode {
		return false, "mode"
	}
	switch e.Mode {
	case "steps":
		if float64(p.steps) < e.Min || float64(p.steps) > e.Max {
			return false, "amount"
		}
	case "timed":
		ms := float64(p.duration / time.Millisecond)
		if ms < e.Min || ms > e.Max {
			return false, "amount"
		}
	}
	return true, ""
}

// --- API client -----------------------------------------------------

var httpClient = &http.Client{Timeout: 90 * time.Second}

func ask(utterance string, qs map[string]question) (*response, time.Duration, error) {
	body, err := json.Marshal(map[string]any{
		"state":     map[string]any{"request": utterance},
		"model":     model,
		"questions": qs,
	})
	if err != nil {
		return nil, 0, err
	}
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		return nil, 0, fmt.Errorf("TYPESAFE_API_KEY is not set")
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
		start := time.Now()
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		latency := time.Since(start)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == 529 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, latency, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 400))
		}
		var out response
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, latency, fmt.Errorf("decode response: %w (%s)", err, truncate(string(raw), 200))
		}
		return &out, latency, nil
	}
	return nil, 0, lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- results --------------------------------------------------------

type planJSON struct {
	Dir        string `json:"dir,omitempty"`
	Mode       string `json:"mode"`
	Steps      int    `json:"steps,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Escalated  bool   `json:"escalated,omitempty"`
}

type itemResult struct {
	Utterance string            `json:"utterance"`
	Expected  exp               `json:"expected"`
	Plan      planJSON          `json:"plan"`
	OK        bool              `json:"ok"`
	Why       string            `json:"why,omitempty"`
	Answers   map[string]answer `json:"answers,omitempty"`
	LatencyMS int64             `json:"latency_ms"`
	InputTok  int               `json:"input_tokens"`
	OutputTok int               `json:"output_tokens"`
	Err       string            `json:"err,omitempty"`
}

// --- main -----------------------------------------------------------

func main() {
	design := flag.String("design", "v3", "question set to evaluate: v1, v2 or v3")
	set := flag.String("set", "tune", "utterance set to run: tune or holdout")
	limit := flag.Int("limit", 0, "only run the first N utterances")
	dry := flag.Bool("dry", false, "print the request body for the first utterance and exit")
	flag.Parse()

	var qs map[string]question
	var resolve func(map[string]answer) plan
	var kindKey string
	switch *design {
	case "v1":
		qs, resolve, kindKey = questionsV1(), resolveV1, "direction"
	case "v2":
		qs, resolve, kindKey = questionsV2(), resolveV2, "movement"
	case "v3":
		qs, resolve, kindKey = questionsV3(), resolveV3, "movement"
	default:
		fmt.Fprintf(os.Stderr, "unknown design %q (want v1, v2 or v3)\n", *design)
		os.Exit(2)
	}

	if *dry {
		body, _ := json.MarshalIndent(map[string]any{
			"state":     map[string]any{"request": items[0].utterance},
			"model":     model,
			"questions": qs,
		}, "", "  ")
		fmt.Println(string(body))
		return
	}

	if os.Getenv("TYPESAFE_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY is not set")
		os.Exit(1)
	}

	run := items
	switch *set {
	case "holdout":
		run = holdoutItems
	case "tune":
	default:
		fmt.Fprintf(os.Stderr, "unknown set %q (want tune or holdout)\n", *set)
		os.Exit(2)
	}
	if *limit > 0 && *limit < len(run) {
		run = run[:*limit]
	}

	var results []itemResult
	for _, it := range run {
		resp, latency, err := ask(it.utterance, qs)
		r := itemResult{Utterance: it.utterance, Expected: it.exp, LatencyMS: latency.Milliseconds()}
		if err != nil {
			r.Err = err.Error()
			fmt.Printf("ERR  %-42q %v\n", it.utterance, err)
			results = append(results, r)
			continue
		}
		p := resolve(resp.Answers)
		r.Answers = resp.Answers
		r.InputTok = resp.Usage.InputTokens
		r.OutputTok = resp.Usage.OutputTokens
		r.Plan = planJSON{Dir: p.dir, Mode: p.mode, Steps: p.steps, DurationMS: p.duration.Milliseconds(), Escalated: p.escalated}
		r.OK, r.Why = eval(p, it.exp)

		mark := "ok  "
		if !r.OK {
			mark = "FAIL"
		}
		extra := ""
		if it.exp.Note != "" {
			extra = "  # " + it.exp.Note
		}
		fmt.Printf("%s %-42q -> %-20s (want %s%s)  %s=%.2f bnd=%.2f  %4dms %3dtok%s\n",
			mark, it.utterance, p, wantString(it.exp), failWhy(r.Why),
			kindKey[:3], resp.Answers[kindKey].Confidence, resp.Answers["bounded"].Noul,
			latency.Milliseconds(), resp.Usage.InputTokens+resp.Usage.OutputTokens, extra)
		results = append(results, r)
	}

	summary(results)

	if raw, err := json.MarshalIndent(results, "", "  "); err == nil {
		path := fmt.Sprintf("/tmp/opencode/nl-eval-%s-%s.json", *design, *set)
		if err := os.WriteFile(path, raw, 0o600); err == nil {
			fmt.Printf("\nraw results: %s\n", path)
		}
	}
}

func wantString(e exp) string {
	if e.Mode == "none" {
		return "decline"
	}
	return strings.Join(e.Dirs, "|") + " " + e.Mode
}

func failWhy(why string) string {
	if why == "" {
		return ""
	}
	return " [" + why + "]"
}

func summary(results []itemResult) {
	var ok, errs int
	var latencies []time.Duration
	var inTok, outTok int
	type counts struct{ ok, bad int }
	byKind := map[string]counts{}

	for _, r := range results {
		if r.Err != "" {
			errs++
			continue
		}
		inTok += r.InputTok
		outTok += r.OutputTok
		latencies = append(latencies, time.Duration(r.LatencyMS)*time.Millisecond)
		c := byKind[r.Expected.Mode]
		if r.OK {
			ok++
			c.ok++
		} else {
			c.bad++
		}
		byKind[r.Expected.Mode] = c
	}
	judged := len(results) - errs
	if judged == 0 {
		fmt.Println("\nno successful requests")
		return
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(p float64) time.Duration {
		i := int(p * float64(len(latencies)-1))
		return latencies[i]
	}

	fmt.Printf("\n=== summary ===\n")
	fmt.Printf("correct:       %d/%d (%.0f%%)\n", ok, judged, 100*float64(ok)/float64(judged))
	if errs > 0 {
		fmt.Printf("errors:        %d\n", errs)
	}
	for _, kind := range []string{"latched", "steps", "timed", "none"} {
		if c, found := byKind[kind]; found {
			fmt.Printf("  %-8s %d/%d\n", kind, c.ok, c.ok+c.bad)
		}
	}
	fmt.Printf("latency:       median %dms  p90 %dms  max %dms\n", pct(0.5).Milliseconds(), pct(0.9).Milliseconds(), pct(1).Milliseconds())
	fmt.Printf("tokens:        %d in + %d out = %d (%.1f in / %.1f out per request)\n",
		inTok, outTok, inTok+outTok,
		float64(inTok)/float64(len(latencies)), float64(outTok)/float64(len(latencies)))
	fmt.Printf("cost:          %.4f USD input (jev-1.13 at $42/Btok; output free)\n",
		float64(inTok)/1e9*42)
}
