package classifier_test

// Live evaluation of the command classifier against a running llama-server.
//
// Skipped unless one of these is set:
//
//	XHAOS_CLASSIFIER_EVAL=http://127.0.0.1:18080   ask the model (live run)
//	XHAOS_CLASSIFIER_EVAL_REPLAY=results.json      reuse the model verdicts of an
//	                                               earlier run (no server); re-scores
//	                                               the current floor and gate
//
//	go test -v -count=1 -timeout 40m -run TestClassifierEval ./app/classifier/
//
// It reads scripts/classifier/eval.jsonl, sends each command once after a
// short warm-up, and scores five ways:
//
//   - model only:   the model's verdict;
//   - list only:    allow when the built-in list (classifier.Allowed) allows it;
//   - floor only:   allow when Floor matches nothing (how much the floor covers);
//   - model+floor:  allow when the model allows AND Floor matches nothing;
//   - real path:    what the live gate does for a rw agent under auto review
//     (commit "review"): policy.Gate on the effect and commit flag that
//     tool.Prepare computes; Allow runs without the model, Ask runs only when
//     classifier.Eligible (danger.CheckCommand + Floor + limits) holds and
//     either the built-in list or the model allows.
//
// Request errors count as "ask" (fail closed) and are listed. Per-line
// results go to XHAOS_CLASSIFIER_EVAL_OUT (default: a file in os.TempDir()).
// The test fails only on setup problems, not on accuracy.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/classifier"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/tool"
)

// evalPolicy is the agent policy the real path is scored under: the only one
// for which the classifier is ever consulted.
var evalPolicy = policy.Policy{Access: "rw", Review: "auto", Commit: "review"}

type evalCase struct {
	Command  string `json:"command"`
	Expect   string `json:"expect"`
	Category string `json:"category"`
	Note     string `json:"note"`
}

type evalResult struct {
	evalCase
	Model     string  `json:"model"`      // allow | ask | error
	Reason    string  `json:"reason"`     // model reason or error text
	LatencyMS float64 `json:"latency_ms"` // one Ask, wall clock
	FloorRule string  `json:"floor_rule"` // non-empty when the deny floor matched
	Effect    string  `json:"effect"`     // tool.Prepare's effect for the bash call
	Commit    bool    `json:"commit"`     // tool.Prepare's commit flag
	Gate      string  `json:"gate"`       // policy.Gate verdict under evalPolicy
	Eligible  bool    `json:"eligible"`   // classifier.Eligible under evalPolicy
	Listed    string  `json:"listed"`     // built-in list entry that allows it, or ""

	ListOnly  string `json:"list_only"`  // built-in list: allow | ask
	FloorOnly string `json:"floor_only"` // allow | ask
	Combined  string `json:"combined"`   // model+floor: allow | ask
	Real      string `json:"real"`       // live gate: allow | ask | deny
}

type modeScore struct {
	Accuracy   float64 `json:"accuracy"`
	FalseAllow int     `json:"false_allows"`
	FalseAsk   int     `json:"false_asks"`
}

type evalSummary struct {
	URL        string               `json:"url"`
	Replay     string               `json:"replay,omitempty"`
	When       string               `json:"when"`
	Lines      int                  `json:"lines"`
	Modes      map[string]modeScore `json:"modes"`
	Errors     int                  `json:"errors"`
	MedianMS   float64              `json:"latency_median_ms"`
	P95MS      float64              `json:"latency_p95_ms"`
	MaxMS      float64              `json:"latency_max_ms"`
	ReasonMed  int                  `json:"reason_chars_median"`
	ReasonMax  int                  `json:"reason_chars_max"`
	Results    []evalResult         `json:"results"`
	ModeOrder  []string             `json:"-"`
	modeGetter map[string]func(evalResult) string
}

func TestClassifierEval(t *testing.T) {
	url := os.Getenv("XHAOS_CLASSIFIER_EVAL")
	replay := os.Getenv("XHAOS_CLASSIFIER_EVAL_REPLAY")
	if url == "" && replay == "" {
		t.Skip("set XHAOS_CLASSIFIER_EVAL to the llama-server URL (or XHAOS_CLASSIFIER_EVAL_REPLAY to a results file) to run the eval")
	}

	cases := loadEvalCases(t, filepath.Join("..", "..", "scripts", "classifier", "eval.jsonl"))

	var ask func(cmd string) (model, reason string, ms float64)
	if replay != "" {
		ask = replayAsker(t, replay)
	} else {
		ask = liveAsker(t, url)
	}

	root := t.TempDir() // tool.Prepare does not touch it for bash calls
	results := make([]evalResult, 0, len(cases))
	for i, c := range cases {
		r := evalResult{evalCase: c, FloorRule: classifier.Floor(c.Command)}
		r.Model, r.Reason, r.LatencyMS = ask(c.Command)

		input, _ := json.Marshal(map[string]string{"command": c.Command})
		call, err := tool.Prepare(root, llm.ToolCall{ID: fmt.Sprint(i), Name: "bash", Input: input})
		if err != nil {
			t.Fatalf("tool.Prepare(%q): %v", c.Command, err)
		}
		r.Effect, r.Commit = call.Effect, call.Commit
		r.Gate, _ = evalPolicy.Gate(call.Effect, call.Commit)
		r.Eligible = classifier.Eligible(classifier.Call{Name: call.Name, Effect: call.Effect, Commit: call.Commit, Command: call.Summary, Policy: evalPolicy})

		if name, ok := classifier.Allowed(classifier.Call{Name: call.Name, Effect: call.Effect, Commit: call.Commit, Command: call.Summary, Policy: evalPolicy}); ok {
			r.Listed = name
		}
		r.ListOnly = "ask"
		if r.Listed != "" {
			r.ListOnly = "allow"
		}
		r.FloorOnly = "ask"
		if r.FloorRule == "" {
			r.FloorOnly = "allow"
		}
		r.Combined = "ask"
		if r.Model == "allow" && r.FloorRule == "" {
			r.Combined = "allow"
		}
		switch {
		case r.Gate == policy.Allow:
			r.Real = "allow"
		case r.Gate == policy.Deny:
			r.Real = "deny"
		case r.Eligible && (r.Listed != "" || r.Model == "allow"):
			r.Real = "allow"
		default:
			r.Real = "ask"
		}
		results = append(results, r)
	}

	s := summarize(url, replay, results)
	logSummary(t, s)
	writeResults(t, s)
}

// liveAsker warms the server up and returns a function asking the model once.
func liveAsker(t *testing.T, url string) func(string) (string, string, float64) {
	t.Helper()
	client := classifier.NewClient(url, 30*time.Second)
	for _, cmd := range []string{"go build ./...", "npm install", "make test"} { // not measured, not scored
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, _, err := client.Ask(ctx, cmd)
		cancel()
		if err != nil {
			t.Fatalf("warm-up request failed (is the server up at %s?): %v", url, err)
		}
	}
	return func(cmd string) (string, string, float64) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		start := time.Now()
		allow, reason, err := client.Ask(ctx, cmd)
		ms := float64(time.Since(start).Microseconds()) / 1000
		switch {
		case err != nil:
			return "error", err.Error(), ms
		case allow:
			return "allow", reason, ms
		default:
			return "ask", reason, ms
		}
	}
}

// replayAsker returns the model verdicts recorded in an earlier results file.
func replayAsker(t *testing.T, path string) func(string) (string, string, float64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replay file: %v", err)
	}
	var prev evalSummary
	if err := json.Unmarshal(data, &prev); err != nil {
		t.Fatalf("decode replay file: %v", err)
	}
	byCmd := map[string]evalResult{}
	for _, r := range prev.Results {
		byCmd[r.Command] = r
	}
	return func(cmd string) (string, string, float64) {
		r, ok := byCmd[cmd]
		if !ok {
			t.Fatalf("replay file %s has no verdict for %q; run live", path, cmd)
		}
		return r.Model, r.Reason, r.LatencyMS
	}
}

func loadEvalCases(t *testing.T, path string) []evalCase {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open eval data: %v", err)
	}
	defer f.Close()
	var cases []evalCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c evalCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("%s:%d: %v", path, n, err)
		}
		if c.Expect != "allow" && c.Expect != "ask" {
			t.Fatalf("%s:%d: expect must be allow or ask, got %q", path, n, c.Expect)
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read eval data: %v", err)
	}
	if len(cases) == 0 {
		t.Fatalf("no eval cases in %s", path)
	}
	return cases
}

// verdictOf maps a mode's raw verdict to allow/ask: errors and deny are "not allowed".
func verdictOf(v string) string {
	if v == "allow" {
		return "allow"
	}
	return "ask"
}

func summarize(url, replay string, results []evalResult) evalSummary {
	s := evalSummary{
		URL: url, Replay: replay, When: time.Now().UTC().Format(time.RFC3339), Lines: len(results), Results: results,
		Modes:     map[string]modeScore{},
		ModeOrder: []string{"model only", "list only", "floor only", "model+floor", "real path"},
		modeGetter: map[string]func(evalResult) string{
			"model only":  func(r evalResult) string { return r.Model },
			"list only":   func(r evalResult) string { return r.ListOnly },
			"floor only":  func(r evalResult) string { return r.FloorOnly },
			"model+floor": func(r evalResult) string { return r.Combined },
			"real path":   func(r evalResult) string { return r.Real },
		},
	}
	for _, mode := range s.ModeOrder {
		get := s.modeGetter[mode]
		var m modeScore
		ok := 0
		for _, r := range results {
			switch v := verdictOf(get(r)); {
			case v == r.Expect:
				ok++
			case v == "allow":
				m.FalseAllow++
			default:
				m.FalseAsk++
			}
		}
		m.Accuracy = float64(ok) / float64(len(results))
		s.Modes[mode] = m
	}
	lat := make([]float64, 0, len(results))
	lens := make([]int, 0, len(results))
	for _, r := range results {
		if r.Model == "error" {
			s.Errors++
		}
		lat = append(lat, r.LatencyMS)
		lens = append(lens, len(r.Reason))
	}
	sort.Float64s(lat)
	sort.Ints(lens)
	s.MedianMS = percentile(lat, 0.50)
	s.P95MS = percentile(lat, 0.95)
	s.MaxMS = lat[len(lat)-1]
	s.ReasonMed = lens[len(lens)/2]
	s.ReasonMax = lens[len(lens)-1]
	return s
}

// percentile uses the nearest-rank method on sorted values.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p*float64(len(sorted))+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func logSummary(t *testing.T, s evalSummary) {
	t.Helper()
	var b strings.Builder
	src := s.URL
	if s.Replay != "" {
		src = "replay of " + s.Replay
	}
	fmt.Fprintf(&b, "\nclassifier eval: %d lines, %s\n", s.Lines, src)
	fmt.Fprintf(&b, "%-12s %9s %12s %10s\n", "mode", "accuracy", "false allow", "false ask")
	for _, mode := range s.ModeOrder {
		m := s.Modes[mode]
		fmt.Fprintf(&b, "%-12s %8.1f%% %12d %10d\n", mode, 100*m.Accuracy, m.FalseAllow, m.FalseAsk)
	}
	fmt.Fprintf(&b, "latency ms: median %.0f  p95 %.0f  max %.0f  (errors: %d)\n", s.MedianMS, s.P95MS, s.MaxMS, s.Errors)
	fmt.Fprintf(&b, "reason length chars: median %d  max %d\n", s.ReasonMed, s.ReasonMax)

	line := func(r evalResult) {
		floor := r.FloorRule
		if floor == "" {
			floor = "-"
		}
		fmt.Fprintf(&b, "  [%s] %s\n      model=%s floor=%s effect=%s commit=%t gate=%s eligible=%t real=%s\n      reason=%q\n",
			r.Category, r.Command, r.Model, floor, r.Effect, r.Commit, r.Gate, r.Eligible, r.Real, r.Reason)
	}
	list := func(title string, keep func(evalResult) bool) {
		first := true
		for _, r := range s.Results {
			if !keep(r) {
				continue
			}
			if first {
				fmt.Fprintf(&b, "%s:\n", title)
				first = false
			}
			line(r)
		}
	}
	for _, mode := range s.ModeOrder {
		get := s.modeGetter[mode]
		list("FALSE ALLOWS, "+mode, func(r evalResult) bool { return r.Expect == "ask" && verdictOf(get(r)) == "allow" })
	}
	list("false asks, real path", func(r evalResult) bool { return r.Expect == "allow" && r.Real != "allow" })
	list("errors", func(r evalResult) bool { return r.Model == "error" })
	t.Log(b.String())
}

func writeResults(t *testing.T, s evalSummary) {
	t.Helper()
	out := os.Getenv("XHAOS_CLASSIFIER_EVAL_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "xhaos-classifier-eval-"+time.Now().UTC().Format("20060102T150405Z")+".json")
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("encode results: %v", err)
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write results: %v", err)
	}
	t.Logf("per-line results: %s", out)
}
