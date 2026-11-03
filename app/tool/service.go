package tool

// Info describes one tool for the owner's tools panel.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Effect      string `json:"effect"` // read, write, or exec: what the policy gate checks
	Source      string `json:"source"` // "built-in"; MCP servers will add their own
}

// Catalog is what agents can call.
type Catalog struct {
	Tools []Info `json:"tools"`
}

// Service lists tools to the client. Agents do not use it.
type Service struct{}

func NewService() *Service { return &Service{} }

func (s *Service) Catalog() Catalog {
	c := Catalog{}
	for _, t := range Specs() {
		c.Tools = append(c.Tools, Info{Name: t.Name, Description: t.Description, Effect: effects[t.Name], Source: "built-in"})
	}
	return c
}
