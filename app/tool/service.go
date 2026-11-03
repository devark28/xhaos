package tool

// Info describes one tool for the owner's tools panel.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Effect      string `json:"effect"` // read, write, or exec: what the policy gate checks
	Source      string `json:"source"` // "built-in"; MCP servers will add their own
}

// Catalog is what agents can call and where they work.
type Catalog struct {
	Root  string `json:"root"`
	Tools []Info `json:"tools"`
}

// Service lists tools to the client. Agents do not use it.
type Service struct{ root string }

func NewService(root string) *Service { return &Service{root: root} }

func (s *Service) Catalog() Catalog {
	c := Catalog{Root: s.root}
	for _, t := range Specs() {
		c.Tools = append(c.Tools, Info{Name: t.Name, Description: t.Description, Effect: effects[t.Name], Source: "built-in"})
	}
	return c
}
