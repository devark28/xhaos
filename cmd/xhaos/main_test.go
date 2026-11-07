//go:build server

package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
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

// serverWide are the bound methods that touch no workspace's data (the
// server's own settings and catalogs). Every other method must name a
// workspace, so the sign-in middleware can hold a session to its own.
var serverWide = map[string]bool{
	"health.Service.Status":        true,
	"workspace.Service.Workspaces": true,                                                                                   // names only, as the picker shows them
	"tool.Service.Catalog":         true,                                                                                   // tool descriptions
	"dictation.Service.Enabled":    true,                                                                                   // whether a speech sidecar is configured
	"dictation.Service.Keeping":    true, "dictation.Service.SetKeeping": true, "dictation.Service.DeleteRecordings": true, // the Developer setting: keep recordings
	"prefs.Service.Get": true, "prefs.Service.Set": true, // appearance, shared by the server
	"usage.Service.Prices": true, "usage.Service.SetPrice": true, "usage.Service.ResetPrice": true, "usage.Service.PriceOf": true, // model prices
}

// Each bound method that is not server-wide takes the workspace: a string
// argument, or a request struct with a WorkspaceID field. A new method that
// forgets it fails here instead of quietly reaching every workspace.
func TestBindingsNameTheirWorkspace(t *testing.T) {
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, b := range bindings(s) {
		typ := reflect.TypeOf(b.Instance())
		short := strings.TrimPrefix(typ.String(), "*")
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			name := short + "." + m.Name
			if serverWide[name] {
				continue
			}
			named := false
			for j := 1; j < m.Type.NumIn(); j++ {
				in := m.Type.In(j)
				if in.Kind() == reflect.Pointer {
					in = in.Elem()
				}
				if in.Kind() == reflect.String {
					named = true
				}
				if in.Kind() == reflect.Struct {
					if _, ok := in.FieldByName("WorkspaceID"); ok {
						named = true
					}
				}
			}
			if !named {
				t.Errorf("%s takes no workspace: give it one, or list it in serverWide if it touches no workspace's data", name)
			}
		}
	}
}
