# Architecture

Xhaos is two parts: a Go server that owns everything that matters (agents, model calls, tools, permissions, secrets, the database) and a browser page that shows it. Closing every browser stops nothing; agents keep working, and a page that comes back catches up.

```
browser ── HTTPS ──▶ reverse proxy ──▶ xhaos (one Go process)
  page (React)                           ├─ auth middleware: session → workspace
  ├─ runtime calls  POST /wails/runtime  ├─ services (app/*), one SQLite database
  └─ live events    GET  /xhaos/events   ├─ run: model turns ──▶ model providers
                                         └─ tool calls ──▶ sandbox ──▶ bash, git, …
```

## Server

`cmd/xhaos` is the only package that imports [Wails v3](https://v3alpha.wails.io); it runs Wails in headless server mode, which serves the page and lets it call the services' exported methods. Everything under `app/` is plain Go with no transport dependency, so it is tested directly. `app/backend` wires the services over one database in a fixed order.

Build tag `server` selects Wails server mode; `embed` (release builds) compiles the built page into the binary. Everything is pure Go, SQLite included (`modernc.org/sqlite`), so cgo is off and the binary is static: it runs on any Linux of its architecture with nothing else installed.

### Packages

`app/` is organized by feature. Each package owns its types, rules, and tables, and the shared pieces are reused rather than duplicated:

| Package | Owns |
|---|---|
| `workspace` | Workspaces; seeds `default`. |
| `agent` | Agents and subagents (integer IDs, parent IDs), `Create`, `Message`, and the workspace `Snapshot`. |
| `policy` | Per-agent policy (`RW + Manual + Review commit` by default) and its ordering; a subagent can't exceed its parent. `Gate` decides allow, ask, or deny for each tool call. |
| `project` | Projects: registered server folders, their file listings, and the default project for `-root`. |
| `mcp` | MCP servers (stdio and Streamable HTTP): configs with sealed secrets, live sessions, and their tools for agents, gated by each server's trust. |
| `mcpserver` | Xhaos as an MCP server (`/mcp`, `/xhaos/mcp`): scoped access tokens, and tools for an MCP client to drive agents, answer questions and use channels for the owner ([mcp.md](mcp.md)). |
| `tool` | Tool definitions, input validation, path confinement, and execution inside the workspace root. |
| `btw` | Side questions about the workspace, answered from a read-only digest; writes nothing. |
| `run` | The turn loop: model call, tool calls through the gate, results back to the model; Stop, and resume after restart. |
| `provider`, `secretbox`, `llm` | Provider configs with API keys sealed at rest, and the provider-neutral model interface with its adapters (`llm/anthropic`, `llm/openai` for OpenAI-compatible endpoints, and `llm/sim`, a scripted stand-in that calls no model). |
| `eventlog` | The shared per-workspace event log with gap-free sequence numbers, plus `EventsAfter`. |
| `idempotency` | The shared retry guard. A retry returns the original result; reusing a key for a different request is a conflict. |
| `sqlitedb`, `apperr` | Opening the database, timestamp helpers, and shared error kinds. |
| `backend` | Composition: migration order and service construction. |
| `auth` | Workspace passphrases (Argon2id, a wrapped data key per workspace), sessions, and the middleware that holds every call and the event stream to its session's workspace. |
| `sandbox`, `egress` | Landlock (and a seccomp socket filter) around agents' commands; the egress proxy that commands holding secrets reach the network through. |
| `secret`, `danger` | The secrets vault (tokens and SSH keys, granted per agent); "dangerously allow" (time-limited, per agent, passphrase-confirmed). |
| `channel`, `memory`, `attachment` | Agent-to-agent channels (and published channel links); workspace memories; uploaded files. |
| `skill`, `web` | Skills in Claude Code's format; `web_fetch` and `web_search`. |
| `live`, `usage`, `prefs` | The Server-Sent Events stream; token counts and prices; interface preferences. |
| `health` | Liveness and version. |

### Events

Only `cmd/xhaos` imports Wails. Every committed event is pushed live as `xhaos:event` (and reply text as `xhaos:delta`) on `/xhaos/events`, a Server-Sent Events stream behind the session check; the Wails event socket carries none of Xhaos's events, because it cannot require a session. A browser holds one stream for all its Xhaos tabs: one tab leads (a lease in localStorage, renewed every 2 s, taken over within 8 s if that tab freezes and at once when it closes) and relays the stream to the others over a BroadcastChannel, so several tabs over plain HTTP do not use up the browser's six connections per server. Each tab loads a snapshot (the agents, and the last 50 messages and tool calls of each, plus any older call still waiting for the owner; `agent.TranscriptBefore` pages back through the rest as a transcript is scrolled up), applies events in sequence order, drops duplicates, and catches up through `EventsAfter` on a gap or when the tab becomes visible again. Board changes (`board.created`, `board.renamed`, `board.deleted`, `board.saved`, the last carrying only the elements a save changed) go out on the same stream. Boards are not part of the agent snapshot: a page loads them with calls and applies the events as they come.

### Workspaces and the call check

Every method the page can call names its workspace, as an argument or a `workspaceId` field; a test in `cmd/xhaos` fails for any bound method that does not (except a short, explicit list of server-wide ones). The auth middleware reads each call before Wails runs it, including calls sent in chunks and arguments in the URL, and refuses any that names a workspace other than its session's. The event stream sends each page only its own workspace's events.

## Page

`frontend/` is TypeScript and React, bundled by esbuild into `frontend/dist/`. Framework-free logic lives in `frontend/src/*.ts` with unit tests; React components live in `frontend/src/ui/`, with one [zustand](https://zustand.docs.pmnd.rs) store split by feature in `ui/state/`, and styles in [StyleX](https://stylexjs.com) next to each component. `frontend/src/server-client.ts` is the browser side of the Wails runtime. See [CONTRIBUTING.md](../CONTRIBUTING.md) for the details that matter when changing it.

## More

- [design-notes.md](design-notes.md): the reasoning Xhaos was designed from.
- [permissions.md](permissions.md): the policy and approval rules.
- [skills.md](skills.md): the skills format.
