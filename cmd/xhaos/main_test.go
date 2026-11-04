//go:build server

package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"xhaos/app/agent"
	"xhaos/app/backend"
)

// Every exported method of a bound service is callable from the page, so
// none may take the acting agent (the page could then speak as any agent) or
// hand out an agent toolset.
func TestBindingsExposeNoAgentSideMethods(t *testing.T) {
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agentType := reflect.TypeOf(agent.Agent{})
	for _, b := range bindings(s) {
		typ := reflect.TypeOf(b.Instance())
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			for j := 1; j < m.Type.NumIn(); j++ {
				if in := m.Type.In(j); in == agentType || in == reflect.PointerTo(agentType) {
					t.Errorf("%s.%s takes an agent.Agent", typ, m.Name)
				}
			}
			for j := 0; j < m.Type.NumOut(); j++ {
				if _, ok := m.Type.Out(j).MethodByName("Execute"); ok {
					t.Errorf("%s.%s returns a toolset (%s)", typ, m.Name, m.Type.Out(j))
				}
			}
		}
	}
}
