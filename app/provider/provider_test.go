package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/provider"
)

type stub struct{}

const ws = "default"

func (stub) Kind() string { return "stub" }
func (stub) Stream(context.Context, llm.Credentials, llm.Turn, func(string)) (llm.Result, error) {
	return llm.Result{}, nil
}
func (stub) Models(_ context.Context, c llm.Credentials) ([]string, error) {
	if c.APIKey != "sk-right-1234" {
		return nil, context.DeadlineExceeded
	}
	return []string{"a", "b"}, nil
}

func TestKeysAreHiddenKeptAndTestable(t *testing.T) {
	dir := t.TempDir()
	s, err := backend.Open(filepath.Join(dir, "x.db"), backend.Options{Adapters: []llm.Adapter{stub{}}, DisableRuns: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "main", Kind: "stub", APIKey: "sk-right-1234", Models: []string{"a", " a ", "b"}})
	if err != nil || !p.HasKey || p.KeyHint != "…1234" || strings.Join(p.Models, ",") != "a,b" {
		t.Fatalf("saved = %+v, %v", p, err)
	}
	// Re-saving without a key keeps the stored one.
	if p, err = s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "main", Kind: "stub", Name: "Renamed"}); err != nil || !p.HasKey || p.Name != "Renamed" {
		t.Fatalf("resave = %+v, %v", p, err)
	}
	if res, _ := s.Providers.Test(ws, "main"); !res.OK || len(res.Models) != 2 {
		t.Fatalf("test = %+v", res)
	}
	list, _ := s.Providers.Providers(ws)
	for _, field := range []string{"sk-right"} {
		for _, p := range list {
			if strings.Contains(p.KeyHint+p.Name+p.BaseURL, field) {
				t.Fatalf("key leaked in %+v", p)
			}
		}
	}
	if _, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "Bad ID", Kind: "stub"}); err == nil {
		t.Fatal("invalid id accepted")
	}
	if _, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "x", Kind: "nope"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "main", Kind: "stub", APIKey: "sk-wrong"})
	if res, _ := s.Providers.Test(ws, "main"); res.OK {
		t.Fatal("wrong key reported OK")
	}
}

func TestOpenAICompatibleProvidersMayBeKeyless(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"qwen3:8b"},{"id":"llama3.3"}]}`)
	}))
	defer srv.Close()
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{DisableRuns: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// No key and no model list: the models come from the endpoint.
	p, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "local", Kind: "openai", Name: "Local", BaseURL: srv.URL + "/v1"})
	if err != nil || p.HasKey || strings.Join(p.Models, ",") != "llama3.3,qwen3:8b" {
		t.Fatalf("saved = %+v, %v", p, err)
	}
	res, err := s.Providers.Test(ws, "local")
	if err != nil || !res.OK || !strings.HasPrefix(res.Message, "Endpoint works") {
		t.Fatalf("test = %+v, %v", res, err)
	}
	// A wrong endpoint with no models listed is refused, not saved empty.
	if _, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "broken", Kind: "openai", BaseURL: srv.URL + "/nope"}); err == nil {
		t.Fatal("saved a provider with no models")
	}
	// Anthropic still needs a key.
	if _, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "a", Kind: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Providers.Test(ws, "a"); res.OK {
		t.Fatal("an Anthropic provider without a key passed the test")
	}
}
