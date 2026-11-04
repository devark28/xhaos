package tool

// Info describes one tool for the owner's tools panel.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Effect      string `json:"effect"` // read, write, or exec: what the policy gate checks
	Source      string `json:"source"` // "built-in", "channels", or "memory" (MCP tools are listed per server)
}

// Catalog is what agents can call.
type Catalog struct {
	Tools []Info `json:"tools"`
}

// Service lists tools to the client. Agents do not use it.
type Service struct {
	// More lists tools that other packages add (channels, memory).
	More func() []Info
}

func NewService() *Service { return &Service{} }

func (s *Service) Catalog() Catalog {
	c := Catalog{}
	for _, t := range Specs() {
		c.Tools = append(c.Tools, Info{Name: t.Name, Description: t.Description, Effect: effects[t.Name], Source: "built-in"})
	}
	if s.More != nil {
		c.Tools = append(c.Tools, s.More()...)
	}
	return c
}
