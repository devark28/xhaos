package usage

import (
	"path/filepath"
	"testing"

	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sqlitedb"
)

func TestReportCostsByModelAndAgent(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	rec, svc := NewRecorder(db), NewService(db)
	// Opus 5.5: 1M prompt tokens of which 800k read from cache, 100k written; 10k out.
	rec.Record("w", 1, "anthropic", "anthropic", "claude-opus-5-5", Turn, llm.Result{InputTokens: 1_000_000, CacheReadTokens: 800_000, CacheWriteTokens: 100_000, OutputTokens: 10_000})
	rec.Record("w", 1, "anthropic", "anthropic", "claude-opus-5-5", Title, llm.Result{InputTokens: 100, OutputTokens: 10})
	rec.Record("w", 2, "ds", "openai", "deepseek-chat", Turn, llm.Result{InputTokens: 5000, OutputTokens: 500})
	rec.Record("w", 0, "anthropic", "anthropic", "claude-opus-5-5", Btw, llm.Result{InputTokens: 0, OutputTokens: 0}) // nothing spent: no row

	rep, err := svc.Usage("w")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Models) != 2 || len(rep.Agents) != 2 || rep.Unpriced != 5500 {
		t.Fatalf("report = %+v", rep)
	}
	opus := rep.Models[0]
	// fresh 100k*$4 + read 800k*$0.20 + write 100k*$5 + out 10k*$20, per million
	// = 0.4 + 0.16 + 0.5 + 0.2 = 1.26; the title adds 100*$4/1e6 + 10*$20/1e6.
	want := 1.26 + 0.0004 + 0.0002
	if opus.Model != "claude-opus-5-5" || opus.Tokens.Cost == nil || abs(*opus.Tokens.Cost-want) > 1e-9 || opus.Purposes[Title] != 1 {
		t.Fatalf("opus = %+v cost %v", opus, *opus.Tokens.Cost)
	}
	if rep.Models[1].Tokens.Cost != nil {
		t.Fatal("a model without a price got a cost")
	}

	// A price set later applies to past use.
	if _, err := svc.SetPrice(Price{Model: "deepseek-chat", Input: 0.28, Output: 0.42}); err != nil {
		t.Fatal(err)
	}
	rep, _ = svc.Usage("w")
	if rep.Unpriced != 0 || rep.Total.Cost == nil || abs(*rep.Total.Cost-(want+(5000*0.28+500*0.42)/1e6)) > 1e-9 {
		t.Fatalf("after pricing: %+v", rep.Total)
	}
	prices, _ := svc.Prices()
	if prices[0].Model != "deepseek-chat" || prices[0].Source != "yours" || abs(prices[0].CacheRead-0.028) > 1e-12 {
		t.Fatalf("prices = %+v", prices[0])
	}
}

func TestLongPromptTier(t *testing.T) {
	p := builtIn["claude-haiku-5-5"]
	short := cost(p, Tokens{Calls: 1, Input: 50_000, Output: 1000})
	long := cost(p, Tokens{Calls: 1, Input: 150_000, Output: 1000})
	if abs(short-(50_000*0.10+1000*0.50)/1e6) > 1e-12 || abs(long-(150_000*0.50+1000*2.50)/1e6) > 1e-12 {
		t.Fatalf("short %v long %v", short, long)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
