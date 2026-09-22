package judge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mheers/tobbie2/internal/tobbie"
)

func TestAskSendsAuthModelAndQuestions(t *testing.T) {
	var gotAuth, gotModel string
	var gotQuestions map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model     string                     `json:"model"`
			Questions map[string]json.RawMessage `json:"questions"`
			State     map[string]any             `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = body.Model
		gotQuestions = body.Questions
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"movement":{"type":"choice","choice":"forward","confidence":0.93}}}`))
	}))
	defer srv.Close()

	c := &Client{apiKey: "test-key", model: "jev-test", endpoint: srv.URL, http: srv.Client()}
	answers, err := c.ask(context.Background(), map[string]any{"request": "vorwärts"}, MoveQuestions())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotModel != "jev-test" {
		t.Errorf("model = %q, want jev-test", gotModel)
	}
	if len(gotQuestions) != 5 {
		t.Errorf("sent %d questions, want 5", len(gotQuestions))
	}
	if answers["movement"].Choice != "forward" {
		t.Errorf("movement = %q, want forward", answers["movement"].Choice)
	}
}

func TestAskRetriesTransientStatus(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"movement":{"type":"choice","choice":"forward","confidence":0.9}}}`))
	}))
	defer srv.Close()

	c := &Client{apiKey: "k", model: "m", endpoint: srv.URL, http: srv.Client()}
	if _, err := c.ask(context.Background(), "x", MoveQuestions()); err != nil {
		t.Fatalf("ask after a 429 should retry and succeed: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server saw %d calls, want 2", got)
	}
}

func TestAskSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad question"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c := &Client{apiKey: "k", model: "m", endpoint: srv.URL, http: srv.Client()}
	_, err := c.ask(context.Background(), "x", MoveQuestions())
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("err = %v, want it to mention 422", err)
	}
}

func TestInterpretMoveEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{
			"movement":{"type":"choice","choice":"pivot","confidence":1.0,"probabilities":{"pivot":1.0}},
			"side":{"type":"choice","choice":"left","confidence":0.99,"probabilities":{"left":0.99}},
			"bounded":{"type":"noul","noul":0.95},
			"walk_amount":{"type":"choice","choice":"1","confidence":0.9,"probabilities":{"1":0.9}},
			"turn_amount":{"type":"choice","choice":"45","confidence":0.93,"probabilities":{"45":0.93}}
		}}`))
	}))
	defer srv.Close()

	c := &Client{apiKey: "k", model: "m", endpoint: srv.URL, http: srv.Client()}
	res, err := c.InterpretMove(context.Background(), "dreh dich ein bisschen nach links")
	if err != nil {
		t.Fatal(err)
	}
	want := tobbie.MoveIntent{Direction: tobbie.DirLeft, Mode: tobbie.MoveTimed, Duration: 375 * time.Millisecond}
	if res.Intent != want {
		t.Fatalf("Intent = %+v, want %+v", res.Intent, want)
	}
	if got := res.Confidence(); got != 0.99 {
		t.Errorf("Confidence() = %.2f, want 0.99", got)
	}
}
