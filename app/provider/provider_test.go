package provider_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"xhaos/app/backend"
	"xhaos/app/llm"
	"xhaos/app/provider"
)

type stub struct{}

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
	p, err := s.Providers.Save(provider.SaveRequest{ID: "main", Kind: "stub", APIKey: "sk-right-1234", Models: []string{"a", " a ", "b"}})
	if err != nil || !p.HasKey || p.KeyHint != "…1234" || strings.Join(p.Models, ",") != "a,b" {
		t.Fatalf("saved = %+v, %v", p, err)
	}
	// Re-saving without a key keeps the stored one.
	if p, err = s.Providers.Save(provider.SaveRequest{ID: "main", Kind: "stub", Name: "Renamed"}); err != nil || !p.HasKey || p.Name != "Renamed" {
		t.Fatalf("resave = %+v, %v", p, err)
	}
	if res, _ := s.Providers.Test("main"); !res.OK || len(res.Models) != 2 {
		t.Fatalf("test = %+v", res)
	}
	list, _ := s.Providers.Providers()
	for _, field := range []string{"sk-right"} {
		for _, p := range list {
			if strings.Contains(p.KeyHint+p.Name+p.BaseURL, field) {
				t.Fatalf("key leaked in %+v", p)
			}
		}
	}
	if _, err := s.Providers.Save(provider.SaveRequest{ID: "Bad ID", Kind: "stub"}); err == nil {
		t.Fatal("invalid id accepted")
	}
	if _, err := s.Providers.Save(provider.SaveRequest{ID: "x", Kind: "nope"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	s.Providers.Save(provider.SaveRequest{ID: "main", Kind: "stub", APIKey: "sk-wrong"})
	if res, _ := s.Providers.Test("main"); res.OK {
		t.Fatal("wrong key reported OK")
	}
}
