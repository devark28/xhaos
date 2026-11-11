package mcpserver_test

// allowLocal lets approved pushes go to the tests' local bare remotes.
func allowLocal(e *env) { e.s.MCPEndpoint.LocalRemotes = true }
