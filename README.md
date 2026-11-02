# Xhaos

A lightweight agent orchestration workspace. This first iteration is a dependency-free browser UI with simulated tasks. No Rust installation, package installation, bundler, or build cache is needed.

## Run

Requires Node.js 20 or newer.

```sh
npm run dev
```

Open http://localhost:5173. Use `PORT=5174 npm run dev` to choose another port. The server binds to loopback only. Stop with Ctrl+C.

```sh
npm run check
npm test
```

## Attachments and Markdown

Both the top composer and the inline new-agent form accept multiple files through the **＋** attachment control, drag/drop, or pasted images. Documents and other file types are preserved as files. Pasted text of 4,000+ characters or 40+ lines becomes a `.txt` attachment automatically. Remove pending files with their × button.

Both inputs share a native `<xhaos-input>` rich-text component. Ctrl/Cmd+B and Ctrl/Cmd+I apply visible bold and italic through browser editing, without showing Markdown markers. There is no formatting toolbar. Rich content serializes to Markdown for task storage and model delivery. Plain Markdown can still be entered or pasted; sent messages render headings, emphasis, lists, quotes, links, inline code, and fenced code. Pasted HTML is not inserted into the editor.

Attachments can be sent with a new task, `/new task`, `tell agent 11 message`, or `/tell 11 message`. Attachment-only submissions create a task to review the files. Files persist as blobs in IndexedDB; task metadata stays in local storage. Expand the task to download attachments or view raster images inline. Files are never uploaded or executed. Document extraction and model delivery await the backend. Storage failures keep the pending attachments for retry. Unsubmitted text and files are session-only and are lost on reload. Clearing browser site data removes stored attachments.

## Prototype interactions

- The agent view opens in a single workspace context. Workspace connection/creation is reserved for a separate entry flow; there is no workspace management rail.
- Two-column mode uses independent sticky stacks: a short column stays visible while a longer column scrolls. Columns taller than the viewport stop when their bottom reaches the viewport bottom. Expanding cards and resizing the window recalculate that limit. Single-column and mobile layouts scroll normally.
- Agent transcripts start collapsed. Subagents stay inside expanded parent cards with indentation and connectors. Parent headers show subagent ID chips; click a chip to open that subagent inside its parent. Use chevrons to expand transcripts, and `/layout 1`, `/layout 2`, or `/layout 3` to choose column count.
- Tool calls and code have no nested boxes; lightweight lexical coloring distinguishes JavaScript, Rust, JSON, shell commands, and diffs. Tool output still expands inline.
- Status uses vivid dots: green for running, blue for complete, yellow for attention. Paused agents show no status dot. Attention uses an expanding ring around a solid dot. New attention requests scroll into view once, without changing keyboard focus or opening collapsed trees. A parent shows an extra attention dot for waiting subagents. Reduced-motion preferences disable animation and smooth scrolling.
- Allow/Deny acts on the request shown on that card. Pause/resume buttons are removed; the legacy slash commands still change mock state. Open the ⋯ menu after the chevron and choose Terminate agent to terminate an agent and its subagents; `/history` retains its transcript and can bring the terminated card back into view without restarting it. Menus also attach/detach recipients. Termination marks the whole subtree as terminated and preserves tasks, attachments, and history. This is local state only until the backend can cancel running work.
- Expand an agent or subagent to read multi-step sample conversations and saved notes inline. Click a tool call or edit to reveal its output or diff. Samples include shell commands, MCP calls, and approval requests; none are executed. Existing seeded tasks receive these sample transcripts without clearing saved state.
- New agent, the `Ctrl/Cmd+Alt+N` shortcut, and `/new` without a task open a draft agent card. Enter the task, choose a single `<model>-<model_id>` profile from the dropdown in the new-agent card header, then press Enter to create or Escape to cancel. Shift+Enter adds a line. The prompt grows with its content and cannot be resized manually. Draft input survives layout changes; creation stores the full prompt and model selection. Dropdown examples include Claude-opus-5.5, GPT-6.1-sol, Claude-haiku-5.5, Claude-sonnet-5.5, DeepSeek-chat, and Local-small-quantized. These are prototype display choices, not live provider discovery or verified API identifiers; backend adapters will map them to configured provider IDs. The selected model ID is saved with the task. Provider adapters remain planned; no task is executed. Settings, MCP information, command help, and voice consent open inline as closable cards in the agent area.
- Click an agent header or its chevron to expand/collapse the transcript. The separate `#ID` button toggles that agent as a recipient chip in the main composer. You can select several agents. Type normally and send to those recipients; detach with × on the chip or click the ID again.
- Create a task with New agent, the `Ctrl/Cmd+Alt+N` shortcut, or the command bar when no recipients are selected. `Ctrl/Cmd+K` focuses the command bar.
- Type `/` to open the command menu. Commands include `/new task`, `/search query` (or `/search` to clear), `/mcp`, `/layout 1|2|3`, `/channels`, `/history`, `/terminate`, `/allow`, `/deny`, and `/help`. `/allow` and `/deny` act on one attached agent with a pending request, or accept an explicit agent ID. `/help status` shows the real status dots and attention pulse; it is also linked from help. Older `/approve` and `/decline` aliases remain supported. Arrow keys navigate the menu; Escape dismisses it. Enter submits the composer; Shift+Enter adds a line.
- The command center occupies roughly the top quarter of the window, above the scrolling agent area. Collapse and layout controls are in the top-right ribbon beside MCP and settings; search is available through `/search`. Neutral surfaces use grayscale; only agent status dots use semantic colors.
- Dictation uses the browser speech API when supported, after an explicit microphone action. Review the transcript before sending. Availability, permissions, and provider processing depend on the browser.

Task data persists in browser local storage, not on a server. Clear the `xhaos-v1` local-storage entry to restore seed data. The demo contains no credentials or real integrations. MCP, agent execution, remote connections, Tauri, and automatic approval classification are not implemented.

See [the architecture direction](docs/architecture.md) for the frontend/backend split.

## Channels

The Channels ribbon button (or `/channels`) opens a read-only view of agent-created subjects, participants, and messages. Agents create and use channels when instructed by you or when they need to coordinate; the user does not create channels, attach them to the composer, or send channel messages. Direct user-to-agent messaging uses the separate agent `#ID` buttons.

No automatic channel creation or delivery runs in this browser prototype. The empty state explains that channels will appear when the backend supplies them. Existing logs from earlier prototype versions remain visible, including legacy user-authored entries. Agent-side channel helpers require a participating creator and writer; actual identity authentication belongs to the backend.

See [architecture](docs/architecture.md) for the intended headless protocol and optional disk-file bridge.

Message updates refresh only affected transcripts and preserve their internal scroll offsets and expanded tool outputs. Full-view rerenders also retain transcript offsets. Most cards still use shared HTML rendering functions; the editor is a native reusable component, without React or added dependencies.

## Typing flow and layout

After clicking a card or the background, printable typing resumes in the last editor at its saved caret. Other editable fields, open menus, selected transcript text, IME composition, and shortcut combinations retain their own behavior. Space/Enter still activate focused buttons. The bare `N` shortcut is removed; use Ctrl/Cmd+Alt+N for a new agent or Ctrl/Cmd+K to focus the main composer.

Agent groups always align at the top, including sparse lists and shorter columns. Longer groups scroll with independent sticky columns.

Planned scope: per-agent session history and a read-only view of agent-managed queues. A harness-managed task queue, per-task checklist UI, and delivery-state dashboard are not currently planned.

`/terminate` targets one attached agent, or accepts an explicit ID. Terminated agents are not resumed by restoring their cards from history.
