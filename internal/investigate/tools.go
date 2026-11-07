package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// Def is a tool as presented to the model.
type Def struct {
	Name        string
	Description string
	// Schema is a JSON Schema object describing the arguments. For
	// mcp-grafana tools it is the server's schema passed through unmodified.
	Schema json.RawMessage
}

// Tool is a named capability the loop can execute on the model's behalf.
type Tool interface {
	Def() Def
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Set is a name-indexed collection of tools that enumerates in sorted order.
// A stable order keeps the tool block byte-identical between requests, which
// prompt caching depends on.
type Set struct {
	byName map[string]Tool
}

// NewSet returns an empty Set.
func NewSet() *Set {
	return &Set{byName: map[string]Tool{}}
}

// Add registers a tool. Duplicate names are rejected.
func (s *Set) Add(t Tool) error {
	name := t.Def().Name
	if name == "" {
		return fmt.Errorf("tool has no name")
	}
	if _, dup := s.byName[name]; dup {
		return fmt.Errorf("duplicate tool %q", name)
	}
	s.byName[name] = t
	return nil
}

// Lookup finds a tool by name. An unknown name is how a hallucinated tool
// call is caught before anything touches the network.
func (s *Set) Lookup(name string) (Tool, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Defs returns the tool definitions sorted by name.
func (s *Set) Defs() []Def {
	defs := make([]Def, 0, len(s.byName))
	for _, name := range s.Names() {
		defs = append(defs, s.byName[name].Def())
	}
	return defs
}

// Len reports how many tools are registered.
func (s *Set) Len() int { return len(s.byName) }

// Names returns the registered tool names, sorted.
func (s *Set) Names() []string {
	return slices.Sorted(maps.Keys(s.byName))
}

// Func adapts a plain function into a Tool.
type Func struct {
	Definition Def
	Run        func(ctx context.Context, args json.RawMessage) (string, error)
}

// Def implements Tool.
func (f Func) Def() Def { return f.Definition }

// Execute implements Tool.
func (f Func) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return f.Run(ctx, args)
}
