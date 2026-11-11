package run_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/classifier"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/provider"
)

var autoDefault = policy.Policy{Access: "rw", Review: "auto", Commit: "review"}

func setDefault(t *testing.T, h *harness, p policy.Policy) {
	t.Helper()
	if _, err := h.s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: ws, AgentSettings: agent.AgentSettings{DefaultPolicy: p}}); err != nil {
		t.Fatal(err)
	}
}

// An agent created the way the page creates it (no policy) with the
// workspace's default on auto review reaches the classifier: the built-in
// list allows a listed command with no model.
func TestDefaultAutoReachesTheClassifier(t *testing.T) {
	h := setupIn(t, t.TempDir(), toolModel([]llm.ToolCall{
		tc("listed", "bash", map[string]any{"command": "gofmt -l ."}),
	}))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	setDefault(t, h, autoDefault)
	a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	if err != nil || a.Policy != autoDefault {
		t.Fatalf("created = %+v, %v", a.Policy, err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if s := h.tools(t, a.ID)["listed"]; s.State != agent.ToolDone || s.By != classifier.By {
		t.Fatalf("listed = %+v; want done, allowed by the classifier", s)
	}
}

// The same with the local model: a command the list does not know is
// allowed by the (fake) model for an agent on the default auto review.
func TestDefaultAutoReachesTheModel(t *testing.T) {
	var asked atomic.Int32
	model := fakeLlama(&asked, "Writes a file in the project.", 0)
	defer model.Close()
	h := setupIn(t, t.TempDir(), toolModel([]llm.ToolCall{
		tc("ok", "bash", map[string]any{"command": "echo ran > out.txt"}),
	}), needNoNet(t), func(o *backend.Options) {
		o.Classifier = classifier.Config{URL: model.URL, Timeout: 5 * time.Second}
	})
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	setDefault(t, h, autoDefault)
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if s := h.tools(t, a.ID)["ok"]; s.State != agent.ToolDone || s.By != classifier.By || !s.ViaModel {
		t.Fatalf("ok = %+v; want done, allowed by the model", s)
	}
	if asked.Load() == 0 {
		t.Fatal("the model was never asked")
	}
}

// On manual review with the classifier on, the first command the classifier
// would have decided under auto says why it asks, once per agent; a command
// it would not decide (a commit) and a classifier switched off get no hint.
func TestManualReviewHintsOnce(t *testing.T) {
	h := setupIn(t, t.TempDir(), toolModel([]llm.ToolCall{
		tc("commit", "bash", map[string]any{"command": "git commit -m x"}),
		tc("first", "bash", map[string]any{"command": "gofmt -l ."}),
		tc("second", "bash", map[string]any{"command": "go vet ./..."}),
	}))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	if a.Policy != policy.Default() {
		t.Fatalf("fresh workspace default = %+v, want policy.Default", a.Policy)
	}
	h.waitStatus(t, a.ID, agent.StatusAttention)
	st := h.tools(t, a.ID)
	if s := st["first"]; s.State != agent.ToolPending || !strings.HasSuffix(s.Reason, "; "+classifier.ManualHint) {
		t.Errorf("first = %+v; want pending with the hint", s)
	}
	for _, id := range []string{"commit", "second"} {
		if s := st[id]; s.State != agent.ToolPending || strings.Contains(s.Reason, classifier.ManualHint) {
			t.Errorf("%s = %+v; want pending without the hint", id, s)
		}
	}

	if err := h.s.Classifier.Owner().SetOn(false); err != nil {
		t.Fatal(err)
	}
	b, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "b", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, b.ID, agent.StatusAttention)
	if s := h.tools(t, b.ID)["first"]; strings.Contains(s.Reason, classifier.ManualHint) {
		t.Errorf("classifier off: first = %+v; want no hint", s)
	}
}

// Changing the policy leaves a waiting call as it is; the next calls use
// the new policy.
func TestSetPolicyAppliesToNextCalls(t *testing.T) {
	h := setupIn(t, t.TempDir(), toolModel(
		[]llm.ToolCall{tc("waits", "bash", map[string]any{"command": "gofmt -l ."})},
		[]llm.ToolCall{tc("next", "bash", map[string]any{"command": "gofmt -l ."})},
	))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	if _, err := h.s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "p", WorkspaceID: ws, AgentID: a.ID, Policy: autoDefault}); err != nil {
		t.Fatal(err)
	}
	if s := h.tools(t, a.ID)["waits"]; s.State != agent.ToolPending {
		t.Fatalf("waiting call after the change = %+v; want still pending", s)
	}
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "waits", Allow: true}); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	st := h.tools(t, a.ID)
	if s := st["waits"]; s.State != agent.ToolDone || s.By == classifier.By {
		t.Errorf("waits = %+v; want done, allowed by you", s)
	}
	if s := st["next"]; s.State != agent.ToolDone || s.By != classifier.By {
		t.Errorf("next = %+v; want done, allowed by the classifier under the new policy", s)
	}
}
