package board

import (
	"reflect"
	"testing"
)

func TestDeletePhrase(t *testing.T) {
	cases := []struct {
		kind   string
		label  bool
		arrows int
		want   string
	}{
		{"rectangle", false, 0, "deleted a rectangle"},
		{"rectangle", true, 0, "deleted a rectangle and its label"},
		{"rectangle", false, 1, "deleted a rectangle and 1 arrow"},
		{"rectangle", false, 2, "deleted a rectangle and 2 arrows"},
		{"rectangle", true, 2, "deleted a rectangle, its label and 2 arrows"},
		{"ellipse", true, 1, "deleted an ellipse, its label and 1 arrow"},
	}
	for _, c := range cases {
		if got := deletePhrase(c.kind, c.label, c.arrows); got != c.want {
			t.Errorf("deletePhrase(%q, %v, %d) = %q, want %q", c.kind, c.label, c.arrows, got, c.want)
		}
	}
}

func TestArrowBoundIDs(t *testing.T) {
	e := element{
		"startBinding": map[string]any{"elementId": "a"},
		"endBinding":   map[string]any{"elementId": "b"},
	}
	if got := arrowBoundIDs(e); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("arrowBoundIDs = %v, want [a b]", got)
	}
	e = element{"startBinding": nil, "endBinding": map[string]any{"elementId": "b"}}
	if got := arrowBoundIDs(e); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("arrowBoundIDs = %v, want [b]", got)
	}
	e = element{}
	if got := arrowBoundIDs(e); got != nil {
		t.Errorf("arrowBoundIDs = %v, want nil", got)
	}
}
