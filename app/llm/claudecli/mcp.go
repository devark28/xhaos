package claudecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// mcpArg is the first argument of the stub MCP server: `xhaos claude-mcp
// <tools.json>`. Claude Code needs an MCP server to learn a tool's name and
// schema; Xhaos runs the tools itself, so the server only describes them and
// answers a call with an error. (With --permission-mode dontAsk Claude Code
// does not even make the call.)
const mcpArg = "claude-mcp"

// MaybeServeMCP runs the stub server when this process was started as Claude
// Code's MCP server; it never returns then. Call it first in main.
func MaybeServeMCP() {
	if len(os.Args) != 3 || os.Args[1] != mcpArg {
		return
	}
	if err := ServeMCP(os.Stdin, os.Stdout, os.Args[2]); err != nil {
		fmt.Fprintf(os.Stderr, "xhaos claude-mcp: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// manifestTool is one tool as Claude Code's MCP client reads it.
type manifestTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ServeMCP speaks newline-delimited JSON-RPC (the MCP stdio transport) until
// in closes, listing the tools in the manifest file.
func ServeMCP(in io.Reader, out io.Writer, manifest string) error {
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var tools []manifestTool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return fmt.Errorf("read %s: %w", manifest, err)
	}
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any) error {
		return enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue // a notification (no id) or noise: nothing to answer
		}
		var err error
		switch req.Method {
		case "initialize":
			version := req.Params.ProtocolVersion
			if version == "" {
				version = "2024-11-05"
			}
			err = reply(req.ID, map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "xhaos", "version": "1"},
			})
		case "tools/list":
			err = reply(req.ID, map[string]any{"tools": tools})
		case "tools/call":
			err = reply(req.ID, map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text", "text": "Xhaos runs this tool itself; this server only describes it."}},
			})
		case "ping":
			err = reply(req.ID, map[string]any{})
		default:
			err = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
		if err != nil {
			return err
		}
	}
	return sc.Err()
}
