# Xhaos

A lightweight agent orchestration workspace: a React browser UI and a headless Go backend (Wails v3 server mode). Without the backend, the UI runs as a local prototype with sample agents.

## Run

Requires Node.js 22.18 or newer (the tests run as TypeScript directly). Run `npm install` once; it installs React, StyleX, TypeScript, and esbuild.

```sh
npm run dev        # prototype UI on http://localhost:5173; rebuilds the bundle on change
npm run build      # one production build of frontend/dist/index.js and index.css
npm run typecheck  # strict TypeScript check of the frontend (tests are only type-stripped)
npm run check      # typecheck, build, plus a syntax check of the dev server
npm test           # unit tests for the frontend logic modules
```

Use `PORT=5174 npm run dev` to choose another port. The dev server binds to loopback only. Stop with Ctrl+C.

The frontend is TypeScript and React, bundled by esbuild into `frontend/dist/index.js` and `index.css` (gitignored):

- `frontend/src/*.ts` are framework-free logic modules (model, questions, markdown, highlight, backend sync, composer intent) with unit tests; `types.ts` holds the shared view and backend shapes.
- `ui/state/` holds the application state in one [zustand](https://zustand.docs.pmnd.rs) store (`store.ts`), with actions split by feature: `composer.ts`, `agents.ts`, `panels.ts`, `questions.ts`, and `backend.ts` (connection and sync). Components subscribe to just the state they render. `ui/keys.ts` installs the page-wide keys (Esc closes the topmost menu or the `/btw` answer).
- `ui/kit.tsx` has the shared building blocks (buttons, status dots, chips, keys); `ui/App.tsx` the page layout and agent grid; `ui/SidePanel.tsx` the right-hand panel; `ui/AgentCard.tsx`, `ui/Transcript.tsx`, `ui/Decisions.tsx`, `ui/Composer.tsx`, `ui/Panels.tsx`, and `ui/QuestionInbox.tsx` the features.
- Styling is [StyleX](https://stylexjs.com): each component defines its styles next to its markup, and colors, fonts, and breakpoints come from `ui/theme/tokens.stylex.ts`. The build compiles them to atomic CSS. `ui/global.css` keeps only the page reset and the markup React does not render itself (Markdown and syntax-highlighted HTML, the `<xhaos-input>` field).

The composer field is still the native `<xhaos-input>` element. It owns its own content, and React never re-renders it.

## Backend (Go, Wails v3 server mode)

Requires Go 1.26+. The backend is Go: `cmd/xhaos` is the Wails headless host and `app/` holds transport-independent feature packages. `frontend/src/server-client.ts` is the browser-side client; under `npm run dev` it finds no Wails runtime and the prototype keeps using mock state.

```sh
scripts/server.sh                                   # flags: -root DIR (optional project), -db PATH, -frontend DIR, -verbose
CGO_ENABLED=1 go test -tags server,purego,libsqlite3 ./...
```

Open http://localhost:5180 (override with `WAILS_SERVER_PORT`). The server serves `frontend/` from disk, so frontend edits need only a reload. When connected, the page shows a toast, sets `<html data-backend="…">`, and exposes `window.xhaos` (for example `await xhaos.call('agent', 'Snapshot', 'default')`). Cards show the backend's agents and transcripts, and creating agents and sending messages go to the server. The prototype's sample agents are mixed in, renumbered from #9000 and marked `· sample`; actions on them stay in the browser. Use `/samples off` or `/samples on` to hide or show them. Attachments are not wired to the backend yet; on real agents they say so.

Models come from providers you add in the Models section of Settings (or `/providers`). Two kinds are supported: Anthropic (an API key) and OpenAI-compatible, one adapter for any endpoint that speaks Chat Completions with streaming: OpenAI, DeepSeek, and local servers such as Ollama, llama.cpp, or vLLM. Presets fill in each one's base URL; add as many OpenAI-compatible providers as you like, each with its own ID. Their key is optional (local servers take none), and leaving the model list empty asks the endpoint which models it offers. Test lists the models to check the key or endpoint. Keys are encrypted at rest and never sent back to the page. The server makes the model calls, so a local endpoint's URL is as seen from the server.

Agents work in projects: folders on the server that you register in the Files tab of the side panel (`-root DIR` registers a folder at startup; without it nothing is registered, and agents can be created once a project exists). Each agent belongs to one project and its tools only reach that folder; agents in different projects coordinate through channels. The Files tab lists the projects, expands each into its folder tree, adds projects by server path, and removes ones no agent uses (the folder itself is untouched). Double-click or Ctrl+click a project (or Alt+Enter) to attach it to the composer, like an agent: new agents then start in it; without one they start in the first project, and subagents always use their parent's. A message never moves an agent to another project: sending with a project attached to agents that work elsewhere is refused with a note naming the agent. Agents have seven tools: `list_dir`, `read_file`, `search`, `write_file`, `edit_file`, `bash`, and `ask_user` (questions for you; see below). File paths are confined to the agent's project, symlinks included, and the file tools cannot write inside `.git` (a hook there would run on commit). `bash` runs in the project but can reach anything the server user can, so each command line is classified first: read-only commands (`ls`, `cat`, `grep`, `rg`, `find` without `-exec`/`-delete`, `git status`/`log`/`diff`/`show`/`branch` listings, and pipes of these) run at once; local git changes (`git add`, `git commit`, `git switch`, `git switch -c`) run without asking under auto review; anything else, including `git push`, always asks. A line is only read or git when every command in it is on those lists and it has no redirection, command substitution, background job, environment prefix, absolute or `..` path, or `git -C`/`-c`/`--output`. MCP servers add more tools. In the Tools and MCP section of Settings (or `/tools`), add a local command (stdio; the Xhaos server starts it as its own user) or a remote URL (Streamable HTTP); env vars and headers are encrypted at rest and never sent back. The server keeps a session per enabled server, lists its tools there, and offers them to agents as `mcp__<server>__<tool>`. Their calls pass the same gate: each server's trust decides how. "Ask every call" (the default) always waits for you, "Run under auto review" behaves like a file write, and "Read-only" runs at once. A server's own read-only hints are shown but never trusted. Every call passes the policy gate first. Reads run at once. With the default policy, writes and commands wait: the card shows each call with its diff or command and Allow/Deny, and `Allow all N` when several wait. A read-only agent has writes and commands denied; `git commit`/`git push` follow the commit setting even under auto review, and a push always asks. Once every waiting call is decided, the allowed ones run in order and the model gets all results (denials as errors). Owner messages sent while calls wait reach the model after the results. Stop (agent menu, or `/pause`) cancels the running turn and kills a running command with its children; the agent pauses and your next message continues it. Terminate ends the agent and its subagents and denies their waiting calls. Agents left running by a stopped server continue on the next start. `/btw <question>` asks the composer's model about your agents ("what is #12 doing?", "summarize everyone's status") from a read-only digest of each agent's status, waiting calls, and recent transcript; the answer shows above the composer and nothing is written to any agent. Agents have no such tool; they will coordinate through channels. The log shows warnings only unless you pass `-verbose`.

Build tags: `server` selects Wails server mode, and `purego` keeps Wails off its cgo GTK webview. `libsqlite3` makes `mattn/go-sqlite3` link the system `libsqlite3` dynamically (it needs `libsqlite3-dev` to build and `libsqlite3-0` at runtime). cgo is on only for SQLite. State lives in SQLite at `~/.local/share/xhaos/xhaos.db`, outside the repo.

`app/` is organized by feature. Each package owns its types, rules, and tables, and the shared pieces are reused rather than duplicated:

| Package | Owns |
|---|---|
| `workspace` | Workspaces; seeds `default`. |
| `agent` | Agents and subagents (integer IDs, parent IDs), `Create`, `Message`, and the workspace `Snapshot`. |
| `policy` | Per-agent policy (`RW + Manual + Review commit` by default) and its ordering; a subagent can't exceed its parent. `Gate` decides allow, ask, or deny for each tool call. |
| `project` | Projects: registered server folders, their file listings, and the default project for `-root`. |
| `mcp` | MCP servers (stdio and Streamable HTTP): configs with sealed secrets, live sessions, and their tools for agents, gated by each server's trust. |
| `tool` | Tool definitions, input validation, path confinement, and execution inside the workspace root. |
| `btw` | Side questions about the workspace, answered from a read-only digest; writes nothing. |
| `run` | The turn loop: model call, tool calls through the gate, results back to the model; Stop, and resume after restart. |
| `provider`, `secretbox`, `llm` | Provider configs with API keys sealed at rest, and the provider-neutral model interface with its adapters (`llm/anthropic`, and `llm/openai` for OpenAI-compatible endpoints). |
| `eventlog` | The shared per-workspace event log with gap-free sequence numbers, plus `EventsAfter`. |
| `idempotency` | The shared retry guard. A retry returns the original result; reusing a key for a different request is a conflict. |
| `sqlitedb`, `apperr` | Opening the database, timestamp helpers, and shared error kinds. |
| `backend` | Composition: migration order and service construction. |
| `health` | Liveness and version. |

Only `cmd/xhaos` imports Wails. Every committed event is pushed live as the Wails event `xhaos:event`. The browser loads a snapshot, applies events in sequence order, drops duplicates, and catches up through `EventsAfter` on a gap or when the tab becomes visible again.

Measured footprint: about 140 MB of modules and 240 MB of build cache in `/tmp`, plus a 17 MB binary.

## Backend direction

Wails v3 with Go is selected for prototyping and the MVP. The planned headless Wails server will serve the complete frontend over HTTP, expose typed Go services through generated Wails bindings, and send live events over Wails WebSockets. This uses Wails' existing client/runtime protocol; the MVP does not need a second custom transport. Keep app-level services, DTOs, idempotency, and durable event sequence independent of Wails so another transport can be added later. A browser disconnect must not stop backend work. Before building, verify Go 1.26+, Wails' platform prerequisites, and free disk; keep temporary Go build/module caches under `/tmp` while disk is constrained. For remote use, add authentication and TLS; origin checks alone do not authenticate users.

## Attachments and Markdown

Both the top composer and the inline new-agent form accept multiple files through the **＋** attachment control, drag/drop, or pasted images. Documents and other file types are preserved as files. Pasted text of 4,000+ characters or 40+ lines becomes a `.txt` attachment automatically. Remove pending files with their × button.

Both inputs share a native `<xhaos-input>` rich-text component. Ctrl/Cmd+B and Ctrl/Cmd+I apply visible bold and italic through browser editing, without showing Markdown markers. There is no formatting toolbar. Rich content serializes to Markdown for task storage and model delivery. Plain Markdown can still be entered or pasted; sent messages render headings, emphasis, lists, quotes, links, inline code, and fenced code. Pasted HTML is not inserted into the editor.

Attachments can be sent with a new task, `/new task`, `tell agent 11 message`, or `/tell 11 message`. Attachment-only submissions create a task to review the files. Files persist as blobs in IndexedDB; task metadata stays in local storage. Expand the task to download attachments or view raster images inline. Files are never uploaded or executed. Document extraction and model delivery await the backend. Storage failures keep the pending attachments for retry. Unsubmitted text and files are session-only and are lost on reload. Clearing browser site data removes stored attachments.

## Prototype interactions

- The agent view opens in a single workspace context. Workspace connection/creation is reserved for a separate entry flow; there is no workspace management rail.
- Two-column mode uses independent sticky stacks: a short column stays visible while a longer column scrolls. Columns taller than the viewport stop when their bottom reaches the viewport bottom. Expanding cards and resizing the window recalculate that limit. Single-column and mobile layouts scroll normally.
- Agent transcripts start collapsed. Subagents stay inside expanded parent cards with indentation and connectors. Parent headers show subagent ID chips; click a chip to open that subagent inside its parent. Use chevrons to expand transcripts, and `/layout 1`, `/layout 2`, or `/layout 3` to choose column count.
- Tool calls and code have no nested boxes; lightweight lexical coloring distinguishes JavaScript, Rust, JSON, shell commands, and diffs. Tool output still expands inline.
- Status uses vivid dots: green for running, blue for complete, yellow for attention. Paused agents show no status dot. Attention uses an expanding ring around a solid dot. New attention requests scroll into view once, without changing keyboard focus or opening collapsed trees. A parent shows only its own status dot; a waiting subagent is indicated only by its own dot (on its header and its ID chip), never by an extra dot on the parent. Reduced-motion preferences disable animation and smooth scrolling.
- Allow/Deny acts on the request shown on that card. Pause/resume buttons are removed; the legacy slash commands still change mock state. Open the ⋯ menu after the chevron and choose Terminate agent to terminate an agent and its subagents; `/history` retains its transcript and can bring the terminated card back into view without restarting it. Menus also attach/detach recipients. Termination marks the whole subtree as terminated and preserves tasks, attachments, and history. This is local state only until the backend can cancel running work.
- Expand an agent or subagent to read multi-step sample conversations and saved notes inline. Click a tool call or edit to reveal its output or diff. Samples include shell commands, MCP calls, and approval requests; none are executed. Existing seeded tasks receive these sample transcripts without clearing saved state.
- The composer is the only way to create agents; there is no New agent button or draft card. With no recipients attached, Enter creates a new agent from the text, and the placeholder says "New agent". With recipients attached, Enter messages them, and the placeholder names them. `/new task` with one agent attached creates a subagent of it. `Ctrl/Cmd+Alt+N` detaches all recipients and focuses the composer. The model for new agents is a borderless dropdown next to the composer's attach button; it is hidden while recipients are attached and is remembered per browser. Anthropic entries are real API model IDs (`claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`); the others are prototype labels until the backend lists configured providers. The selection is saved with the agent; on the backend page the list comes from configured providers and the agent runs on that model.
- Click an agent header or its chevron to expand/collapse the transcript. Double-click (or Ctrl/Cmd+click, which never waits) the agent header to attach or detach it as a composer recipient; with the header focused, `Alt+Enter` does the same. The `#ID` is a label, not a separate button. You can select several agents, then send normally; detach with × on a recipient chip or repeat the attach action.
- `Ctrl/Cmd+K` focuses the composer.
- Type `/` to open the command menu. Commands include `/new task`, `/search query` (or `/search` to clear), `/mcp`, `/memory`, `/layout 1|2|3`, `/channels`, `/history`, `/terminate`, `/allow`, `/deny`, and `/help`. `/allow`, `/deny`, `/pause`, and `/resume` act on eligible attached agents, or accept one explicit agent ID. Ineligible targets are skipped and reported. Questionnaires require answers, not approval; `/allow` and `/deny` never answer or dismiss them. `/help status` shows the real status dots and attention pulse; it is also linked from help. Older `/approve` and `/decline` aliases remain supported. Arrow keys navigate the menu; Escape dismisses it. Enter submits the composer; Shift+Enter adds a line.
- The page has two columns. On the left, the composer sits above the scrolling agent area, with Expand/Collapse all and the 1/2/3-column layout buttons in a bar above the agents; search is available through `/search`. The logo sits at the top left, beside the Message/Draw tabs. On the right, the side panel is always open at 30% of the width; its tab icons (Questions, Files, Channels, Memories, Settings; Settings has Models and Tools and MCP as sections that expand in place) stay at its top and switch what it shows. On narrow screens it sits below the agents. Panels never replace the composer, so you can keep typing while one is open. Neutral surfaces use grayscale; only agent status dots use semantic colors.
- Dictation uses the browser speech API when supported, after an explicit microphone action. Review the transcript before sending. Availability, permissions, and provider processing depend on the browser.

Task data persists in browser local storage, not on a server. Clear the `xhaos-v1` local-storage entry to restore seed data; workspace memory notes use `xhaos-memory-v1`. The demo contains no credentials or real integrations. MCP, agent execution, remote connections, Wails integration, and automatic approval classification are not implemented.

See [the architecture direction](docs/architecture.md) for the frontend/backend split.

## Channels

The Channels icon in the side panel (or `/channels`) opens a read-only view of agent-created subjects, participants, and messages. Agents create and use channels when instructed by you or when they need to coordinate; the user does not create channels, attach them to the composer, or send channel messages. Direct user-to-agent messaging uses the separate agent `#ID` buttons.

No automatic channel creation or delivery runs in this browser prototype. The empty state explains that channels will appear when the backend supplies them. Existing logs from earlier prototype versions remain visible, including legacy user-authored entries. Agent-side channel helpers require a participating creator and writer; actual identity authentication belongs to the backend.

See [architecture](docs/architecture.md) for the intended headless protocol and optional disk-file bridge.

React updates keep transcript scroll offsets and expanded tool outputs, and an open panel survives live updates.

- **Clearing the composer:** press Esc twice quickly to clear its text. Attachments stay. A single Esc closes the slash-command menu.
- **Working indicator:** while an agent is running and no reply text has arrived yet, its transcript shows a rotating verb ("Thinking…", "Drafting…") with elapsed seconds, and its header shows the verb next to the status dot. Streaming replies end with a blinking caret.
- **Accent colour:** the violet `brand` colour in `ui/theme/tokens.stylex.ts` marks things that need you: the Questions tab, "Questions waiting", Allow buttons, approval requests, and the questionnaire. Status dots keep their semantic colours.

## Typing flow and layout

After clicking a card or the background, printable typing resumes in the last editor at its saved caret. Other editable fields, open menus, selected transcript text, IME composition, and shortcut combinations retain their own behavior. Space/Enter still activate focused buttons. The bare `N` shortcut is removed; use Ctrl/Cmd+Alt+N for a new agent or Ctrl/Cmd+K to focus the main composer.

Agent groups always align at the top, including sparse lists and shorter columns. Longer groups scroll with independent sticky columns.

Planned scope: per-agent session history and a read-only view of agent-managed queues. A harness-managed task queue, per-task checklist UI, and delivery-state dashboard are not currently planned.

`/terminate` targets one attached agent, or accepts an explicit ID. Terminated agents are not resumed by restoring their cards from history.

## Questions from agents

Agents ask you questions with the `ask_user` tool: one to six questions, each with options (one or several may be chosen) and/or a free-text answer, and optionally skippable. No permission is involved; the agent's turn pauses, its card shows it needs you (with no Allow/Deny), and the **Questions** tab in the side panel shows a badge. Your answers go back to the model as the tool's result and the agent continues; Dismiss tells it you won't answer. The transcript notes "Asked you: …" and, once answered, the answers. Questions never take over the composer. The Questions view has horizontal short-title tabs. Only the active question and its options/free-text answer are shown. Custom answer fields grow with content and cannot be resized manually. The Review tab shows the response overview; Enter submits that agent's batch, then opens the next waiting agent. There is no send button and no cross-agent combined submission. Enter advances through questions; Shift+Enter adds a line in a custom answer. Draft answers persist between tabs and message view.

The sample planning agent's questions are simulated (prototype data).

## Channels and subagents

Agents talk to each other through channels, using the tools `agents_list`, `channel_create`, `channel_send`, `channel_list` and `channel_read`. A message sent to a channel lands in every other member's transcript, marked as coming from that channel, and wakes them. Who may join whom:

- a top-level agent: any other top-level agent (in any project) and its own subagents;
- a subagent: its parent and its siblings only. Its parent can still add it to one of the parent's own channels.

`spawn_agent` starts a subagent in the parent's project, with the parent's model and policy. Its review mode is the same as the parent's write gate, so under manual review you approve each spawn. The subagent gets a channel with its parent, and its final answer is posted there automatically, waking the parent. A channel accepts at most 30 messages per 10 minutes, so two agents can't keep answering each other forever. The Channels tab shows every channel and its messages live, read-only.

## Memory and compaction

Each agent's context is kept within its model's budget. A provider's context size (Settings → Models; empty uses 200,000 tokens for Anthropic and 128,000 for OpenAI-compatible) is the budget; when a request nears 80% of it, measured from the token counts providers report, the agent's own model summarizes the older part of the conversation. The cut falls only where nothing is split: before a user message, or after a finished round of tool calls. The model then sees the summary and the recent messages; the event log keeps the full history, and the transcript shows a "Context compacted" note with the summary. A provider error saying the request is too long also triggers a compaction and one retry. Later compactions fold in the previous summary.


The Memories icon in the side panel opens the current workspace's compact memory panel. Add, edit, and delete notes without selecting a scope; all agents in that workspace share them. Enter saves, Shift+Enter adds a line, and Escape cancels editing. Notes are saved in this browser only; backend storage and context retrieval are planned. Compaction summarizes an ongoing session so it can continue with less conversation in active context. Durable memory is selected knowledge reused across sessions, such as preferences, project conventions, and decisions. A summary or memory must not replace full session history or grant permissions.

## Sketching

The composer's Draw tab is an [Excalidraw](https://excalidraw.com) board in dark mode. It loads on first use (its code is a separate chunk) and serves its fonts from this server, so nothing is fetched from a CDN. The board is kept in this browser between visits. Attach the drawing to your next message as an image (PNG) or as Excalidraw data: the `.excalidraw` scene JSON, which a model can read and edit like code. Attachments are stored in this browser and not yet sent to backend agents.

## Skills

Skills are planned backend instruction packages, with user/workspace/project sources and per-agent activation. See [skills design](docs/skills.md); no skill loader is implemented in the browser prototype.
