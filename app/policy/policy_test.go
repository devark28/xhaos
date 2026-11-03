package policy

import "testing"

func TestWithin(t *testing.T) {
	ro := Policy{Access: "ro", Review: "manual", Commit: "blocked"}
	cases := []struct {
		name         string
		child, limit Policy
		want         bool
	}{
		{"equal", Default(), Default(), true},
		{"more restrictive", ro, Default(), true},
		{"write beyond read-only parent", Default(), ro, false},
		{"auto beyond manual", Policy{"rw", "auto", "review"}, Default(), false},
		{"commit beyond review", Policy{"rw", "manual", "allowed"}, Default(), false},
	}
	for _, c := range cases {
		if got := c.child.Within(c.limit); got != c.want {
			t.Errorf("%s: Within = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestValidateRejectsUnknownValues(t *testing.T) {
	if err := (Policy{"rw", "manual", "sometimes"}).Validate(); err == nil {
		t.Fatal("expected an error for an unknown commit value")
	}
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		p      Policy
		effect string
		commit bool
		want   string
	}{
		{Default(), EffectRead, false, Allow},
		{Default(), EffectWrite, false, Ask},
		{Default(), EffectExec, true, Ask},
		{Policy{"ro", "auto", "allowed"}, EffectRead, false, Allow},
		{Policy{"ro", "auto", "allowed"}, EffectWrite, false, Deny},
		{Policy{"ro", "auto", "allowed"}, EffectExec, false, Deny},
		// Auto review: file writes and local git run; other commands still ask.
		{Policy{"rw", "auto", "review"}, EffectWrite, false, Allow},
		{Policy{"rw", "auto", "review"}, EffectGit, false, Allow},
		{Policy{"rw", "auto", "review"}, EffectExec, false, Ask},
		{Policy{"rw", "auto", "review"}, EffectGit, true, Ask},
		{Policy{"rw", "auto", "allowed"}, EffectGit, true, Allow},
		{Policy{"rw", "auto", "allowed"}, EffectExec, true, Ask}, // a push always asks
		{Policy{"rw", "auto", "blocked"}, EffectGit, true, Deny},
		{Policy{"rw", "manual", "allowed"}, EffectGit, false, Ask},
		{Policy{"ro", "auto", "allowed"}, EffectGit, false, Deny},
	}
	for _, c := range cases {
		if got, _ := c.p.Gate(c.effect, c.commit); got != c.want {
			t.Errorf("%+v.Gate(%s, %v) = %s, want %s", c.p, c.effect, c.commit, got, c.want)
		}
	}
}
