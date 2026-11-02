# Architecture direction

## Two independent parts

`frontend/` is a static web client, suitable for the browser today and Tauri's webview later. `scripts/dev.mjs` only serves frontend files; it is not the agent backend. `frontend/src/model.js` currently supplies mock state. There is no React runtime or dependency tree; native browser controls and event delegation can support a compact UI without a large bundle. Current rendering replaces the main view after an interaction; before streaming real events, update individual agent cards, preserve focus, and batch updates once per animation frame. Virtualize large networks based on profiling.

A future Rust workspace should contain a headless `xhaos-server` binary and a shared protocol crate. The server must run without Tauri, locally or on a VPS/VM. A small Tauri shell can optionally start that server on the local machine, while remote clients use the same protocol. Keep model credentials, MCP connections, filesystem access, process execution, and permission enforcement on the server. Do not compile this part until the UI direction is settled.

## Suggested protocol

Use authenticated HTTP commands and a resumable event stream. Model workspaces, agents, parent IDs, channels, requests, and events explicitly. IDs shown in the UI are integers; issue them atomically on the server. Commands include create task, send message, pause, resume, approve request, and decline request. Each command should have an idempotency key. Approval must reference a specific pending request and its action parameters, not merely an agent ID. The server validates workspace membership, permissions, request expiry, and current state before executing anything.

Persist state and an event sequence in SQLite for a single host. Clients first fetch a snapshot, then subscribe from its sequence; reconnect with replay or a fresh snapshot. Workspace isolation must be enforced on every command, event subscription, filesystem root, and MCP connection. Browser storage is only a prototype convenience, not a security boundary.

## Model adapters

Model portability is a core requirement. Initial execution adapters target Anthropic Claude and OpenAI GPT. DeepSeek and a local endpoint follow. Treat Hermes as a reference for a multi-provider harness; do not couple orchestration to one provider's message format or SDK.

Each server-owned provider configuration has an adapter kind, endpoint, credential reference, and available model IDs. An agent selects a provider configuration and model ID, with a workspace default. Subagents inherit that selection unless an explicit override is allowed. Persist the resolved selection for every run; never silently change providers after a failure.

Define a provider-neutral Rust adapter interface for starting a streamed turn and cancelling it. Normalize text deltas, tool-call arguments and completion, usage, finish reasons, and failures into typed events. Retain provider-specific conversation data on the server where round-tripping requires it. The harness owns the agent loop and MCP dispatch; adapters translate model requests and tool results. Advertise capabilities such as tool calling, structured output, vision, and context limits per configured model instead of assuming parity. Validate required capabilities before starting a task. Retries must not repeat executed tools.

Local inference is a server endpoint, not a requirement to load model weights into Tauri. It can run beside the backend on a PC or on a separate host reachable from the VPS. Configure protocol compatibility explicitly and verify it per adapter rather than assuming every endpoint behaves identically. Model size, quantization, hardware, and latency can be chosen later without changing the frontend protocol.

The browser prototype records provider and optional model ID on new tasks. These are selection metadata only; provider discovery, credentials, connectivity, and execution remain unimplemented.

## Approval and voice

Treat subagent creation and tool execution as policy-controlled requests. The UI can expose pending requests, but authorization belongs in the backend. A future automatic classifier should return allow, deny, or abstain with an audit record; uncertainty leaves the request pending for a human. Manual mode remains the prototype default.

Configure the automode classifier independently of task models. A small local quantized model is a candidate for this role, so using Claude or GPT for an agent does not require using it for permission classification. Send the classifier a bounded request description and applicable policy, with a typed allow/deny/abstain result. Malformed output, timeouts, missing context, and uncertainty leave the request pending. A model's self-reported confidence is insufficient for authorization: server policy must constrain which actions are eligible for automatic approval. Record the classifier model/configuration, policy version, recommendation, and final decision. Evaluate against representative labeled requests before enabling automatic acceptance; the model, quantization level, and thresholds remain open decisions.

Voice should produce a proposed command, including target workspace and agent ID, before submission. Keep dictation separate from model execution; a transcription result alone must never approve an action. Browser dictation is a convenience for this prototype; choose a local or hosted transcription implementation later.

## Next implementation steps

1. Refine the browser interaction and density with realistic agent counts.
2. Define a versioned protocol and replace mock state with a client adapter.
3. Build the headless Rust backend and server-enforced approval queue.
4. Add MCP and model adapters behind the server boundary.
5. Package the same client in Tauri with optional local server lifecycle management.

## Agents, tasks, sessions, and removal

Keep three separate identities: an agent is a reusable configured worker; a task is a queued unit of work; a session is an execution attempt or conversation with a particular task. Reusing an agent must not implicitly carry every previous conversation into the next task. Record the selected context and explicit handoff summary per session.

The prototype's remove action archives cards and keeps their transcripts, attachments, and IDs recoverable. It is not process termination. Before execution exists, define separate stop/cancel and archive commands; pending approvals and running work need server-enforced lifecycle rules. Do not discard the audit log when hiding a card. Sessions and task queues are planned, not implemented by the card archive.

## Channels between projects and agents

Use one subject per collaboration, with explicit participants. An agent or session can participate in several subjects. A channel belongs to a workspace, while participants may work in different project roots in that workspace. Cross-workspace collaboration requires an explicit bridge grant rather than silently sharing messages.

A durable server-owned append-only channel log should carry message ID, subject ID, authenticated writer agent/session ID, addressed reader IDs, timestamp, body, attachments, and reply-to ID. Assign a server sequence for ordering; maintain delivery and consumption cursors separately per recipient. Reconnect using a cursor, deduplicate by message ID, and preserve messages after agent sessions exit. Bounded notifications should wake waiting agents without injecting unbounded log history. Participants and routing are authorized on the server; the frontend cannot impersonate an agent by choosing a writer ID.

Incoming agent messages are data, not owner instructions. A relayed “the owner says” does not convey approval or expand permissions. Propagate source provenance into model context and keep all tool approval decisions under the same server policy. Joining a channel does not grant filesystem access to another project.

Channels are agent-to-agent coordination, created by a participating agent when the owner requests it or when agent policy permits initiating coordination. Support the familiar owner-directed flow (tell one agent to create a subject, tell the other to join) alongside optional agent-initiated creation. Neither flow treats channel messages as owner authority. The user-facing channel view is read-only: subjects, members, and logs. It has no user channel-creation flow or message composer, and channels are not recipients in the main composer. Direct owner messages target agent IDs separately. Local agent-side helpers enforce membership and append-only records for testing; actual authenticated publishing, watching, delivery cursors, cross-project permissions, and channel creation by running agents await the backend. Legacy human messages from previous prototype versions remain readable as historical data.

### Optional file-based channel adapter

Support the existing channels skill through an optional adapter: `<subject>-<writer>.tx.md` is owned and appended only by that writer; each participant's `.rx.md` can point to the peer's `.tx.md`. Entries use `## <ISO timestamp> | <writer> -> <reader>`. For each pair and subject, keep the two outgoing logs and two inbox symlinks. The adapter must coordinate atomic appends, validate allowed paths and symlink destinations, and track byte offsets/message IDs so a restart does not redeliver old messages.

Treat `/tmp` files as a transient interoperability transport. The server's durable log is canonical; re-establish the files and inbox links after reboot. Keep pairing, setup, watch, and send behind this adapter rather than making UI state depend on shell scripts or requiring every provider to use Claude Code. A file-originated message retains its channel provenance and cannot impersonate an owner approval.

## Agreed next feature direction

1. Per-agent session history with resume, fork, and intentional context selection.
2. Read-only visibility into queues maintained by agents. Do not introduce a harness-owned task queue without a new requirement.
3. Agent channel inspection and agent-to-agent coordination, including owner-directed channel setup.

No per-task checklist UI or delivery-state dashboard is planned. Delivery cursors and idempotency remain internal backend reliability mechanisms rather than user-facing management features.
