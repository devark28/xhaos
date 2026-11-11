# Design notes

These are the notes Xhaos was designed from, written before the backend existed. They explain why things are the way they are (server-owned execution, the approval gate, channels as data rather than instructions, memory versus compaction). Some passages describe the browser prototype or plans that have since been built or changed; for how the code is organized today, see [architecture.md](architecture.md), and for what the product does, the [user guide](guide.md).

## Two independent parts

`frontend/` is a TypeScript React web client bundled by esbuild into `frontend/dist/index.js` and `index.css` (styles via StyleX). The Go server serves it, or `scripts/dev.mjs` does for prototype work; neither is the agent backend in prototype mode. React components live in `frontend/src/ui/`, and framework-free logic modules (model, questions, backend sync, composer intent) stay in `frontend/src/` so they remain unit-testable. Live reply text repaints at most once per animation frame. Virtualize large networks based on profiling.

Wails v3 with Go is the chosen framework and language for the prototype and MVP. Use Wails headless server mode to serve the frontend over HTTP and expose Go services through generated bindings, with Wails events over WebSocket. This lets the browser receive the complete UI from the backend and avoids writing a custom transport for the first version. The current browser-only UI remains unchanged while the backend is built.

The server owns runtime lifetime: closing or disconnecting a browser client does not stop agents or change their saved policies. Server-side automode continues eligible work unattended; manual requests remain pending and block only dependent work. Reconnection restores current sessions and missed events. Keep model credentials, MCP connections, filesystem access, process execution, and permission enforcement on the server. Wails' server build is the MVP headless host; later, a Wails desktop shell may connect to the same local server. Do not make the desktop window the owner of execution lifetime.

## Client/backend contract

For the MVP, use Wails service bindings as the frontend-to-Go RPC mechanism and Wails WebSocket events for live updates. Define app-level service interfaces and typed DTOs independently of Wails so domain behavior stays testable and can gain another transport later. Model workspaces, agents, parent IDs, channels, requests, and events explicitly. IDs shown in the UI are integers; issue them atomically on the server. Commands include create task, send message, pause, resume, approve request, and decline request. Each mutation should have an idempotency key. Approval must reference a specific pending request and its action parameters, not merely an agent ID. The server validates workspace membership, permissions, request expiry, and current state before executing anything.

Persist state and a monotonically increasing event sequence in SQLite for a single host. On connect or reconnect, the client fetches a snapshot or asks for events after its last sequence, then deduplicates live WebSocket events by sequence. Wails events are a live notification path; durable history and replay come from the application service and database. Workspace isolation must be enforced on every command, event subscription, filesystem root, and MCP connection. Browser storage is only a prototype convenience, not a security boundary.

The headless Wails server should serve the UI and runtime from the same origin. Wails server mode's binding/runtime protocol is the MVP transport, not a public language-neutral API. Do not implement a second custom wire protocol now. If later clients need independent access (such as a TUI, non-Wails desktop shell, or third-party integration), add a documented `/api/v1` HTTP/WebSocket adapter over the same Go services. For VPS deployment, add real authentication and authorization, TLS or a trusted TLS reverse proxy, and restrictive origin configuration; origin checks alone are not authentication.

## Model adapters

Model portability is a core requirement. Initial execution adapters target Anthropic Claude and OpenAI GPT. DeepSeek and a local endpoint follow. Treat Hermes as a reference for a multi-provider harness; do not couple orchestration to one provider's message format or SDK.

Each server-owned provider configuration has an adapter kind, endpoint, credential reference, and available model IDs. An agent selects a provider configuration and model ID, with a workspace default. Subagents inherit that selection unless an explicit override is allowed. Persist the resolved selection for every run; never silently change providers after a failure.

Define a provider-neutral Go adapter interface for starting a streamed turn and cancelling it. Normalize text deltas, tool-call arguments and completion, usage, finish reasons, and failures into typed events. Retain provider-specific conversation data on the server where round-tripping requires it. The harness owns the agent loop and MCP dispatch; adapters translate model requests and tool results. Advertise capabilities such as tool calling, structured output, vision, and context limits per configured model instead of assuming parity. Validate required capabilities before starting a task. Retries must not repeat executed tools.

Local inference is a server endpoint, not a requirement to load model weights into Wails. It can run beside the backend on a PC or on a separate host reachable from the VPS. Configure protocol compatibility explicitly and verify it per adapter rather than assuming every endpoint behaves identically. Model size, quantization, hardware, and latency can be chosen later without changing the frontend contract.

The browser prototype records provider and optional model ID on new tasks. These are selection metadata only; provider discovery, credentials, connectivity, and execution remain unimplemented.

## Approval and voice

Treat subagent creation and tool execution as policy-controlled requests. The UI can expose pending requests, but authorization belongs in the backend. A future automatic classifier should return allow, deny, or abstain with an audit record; uncertainty leaves the request pending for a human. Manual mode remains the prototype default.

Configure the automode classifier independently of task models. A small local quantized model is a candidate for this role, so using Claude or GPT for an agent does not require using it for permission classification. Send the classifier a bounded request description and applicable policy, with a typed allow/deny/abstain result. Malformed output, timeouts, missing context, and uncertainty leave the request pending. A model's self-reported confidence is insufficient for authorization: server policy must constrain which actions are eligible for automatic approval. Record the classifier model/configuration, policy version, recommendation, and final decision. Evaluate against representative labeled requests before enabling automatic acceptance; the model, quantization level, and thresholds remain open decisions.

Voice should produce a proposed command, including target workspace and agent ID, before submission. Keep dictation separate from model execution; a transcription result alone must never approve an action. Browser dictation is a convenience for this prototype; choose a local or hosted transcription implementation later.


## Agents, tasks, sessions, and removal

Keep three separate identities: an agent is a reusable configured worker; a task is a queued unit of work; a session is an execution attempt or conversation with a particular task. Reusing an agent must not implicitly carry every previous conversation into the next task. Record the selected context and explicit handoff summary per session.

The prototype’s Terminate action marks an agent and its descendants terminated and hides its card while retaining transcripts, attachments, and IDs. It only changes local state; no backend process exists to cancel yet. Restoring a card must not resume the terminated session. Before execution exists, define separate stop/cancel and archive commands; pending approvals and running work need server-enforced lifecycle rules. Do not discard the audit log when hiding a card. Sessions and task queues are planned, not implemented by the card archive.

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
4. Durable, inspectable memory, kept separate from per-session compaction summaries.

No per-task checklist UI or delivery-state dashboard is planned. Delivery cursors and idempotency remain internal backend reliability mechanisms rather than user-facing management features.

The next execution-policy and first-dogfooding contract is recorded in [permissions](permissions.md).

## Structured questions

Expose a backend tool for requesting a batch of questions: stable request ID, agent/session ID, questions with stable IDs, prompt, options, single/multiple selection, required flag, and optional custom answer text. Persist requests while no frontend is connected. Elevate questions to the composer question inbox, separate from permission requests. Use horizontal question-title tabs, one full question at a time, then a per-agent response overview. Enter submits that agent's batch and advances to the next waiting agent. Cards link back to the inbox; normal message drafts remain available in a separate composer view.

Submit one structured answer batch against the immutable request ID. Validate every required answer and all option IDs before accepting anything; deduplicate retries and reject stale/cancelled/previously resolved batches. Record provenance and the answer event in session history, then unblock only the waiting turn. Pending questions are not tool approvals: allow/deny commands and an approval classifier cannot silently answer them. Termination cancels outstanding questions. Optional policy for another agent to answer questions must be an explicit future capability, not inferred from frontend absence.

## Memory and context compaction

Compaction and memory solve related but different problems. A compaction summary carries the current session forward when its conversation is shortened; it should preserve the active task, decisions, constraints, and unresolved work, with links to the source events. Durable memory stores selected knowledge for reuse across sessions, such as owner preferences, project conventions, architecture decisions, and lessons learned. Neither mechanism should silently replace the full session history.

Keep memory entries explicit, inspectable, editable, deletable, and attributable to their source (owner, agent, or imported skill), with created/updated timestamps. Memory belongs to a workspace and is shared by its agents; the current workspace supplies scope, so the owner does not choose it for each entry. Retrieve only relevant entries when building a turn, and show which memories were included. Treat agent-written memories as suggestions subject to a configurable save policy; never let memory grant tools or permissions. Surface stale or conflicting memories with provenance and recency so the owner can resolve them. Memory retrieval should work independently of provider adapters, and compaction summaries should remain session records rather than being promoted automatically to durable memory.

The UI should provide a small workspace memory view and a way to inspect relevant memories from an agent or session. Do not turn memory into another dashboard or inject the entire store into every prompt. The browser prototype has a local-only Memories panel; backend storage and context retrieval are not implemented.

## Drawings in the composer

The lightweight SVG sketchpad in the browser prototype is temporary. Target an Excalidraw-style canvas embedded in the composer, using the Obsidian plugin workflow as the reference: switch between writing and drawing in the same task, attach a drawing to the prompt, and reopen/edit it later from the task or session. Preserve the editable vector scene as the attachment's canonical data and include a rendered preview for providers that accept images; do not reduce the saved drawing to a PNG alone. Keep the scene format and editor boundary shared between browser and Wails so the drawing engine can be replaced without changing task/session APIs. The current prototype only exports PNG and is not the final interaction or storage format. Status: the composer's Draw tab is now Excalidraw, and its boards are stored on the server (`app/board`, per workspace, with images kept by content hash). Attaching a board's scene to a task and reopening it from there are still to do.

## Skills

User-defined instruction packages are a first-class requirement, separate from tools and execution permissions. See [skills](skills.md) for package discovery, per-agent activation, versioned loading, and the channels example. Skill loading belongs to the backend and must work with every model adapter.

## Sealed commands: reaching only the proxy

Status: open. A sealed command (one given secrets) may connect over TCP only to the egress proxy's port, by a Landlock rule. Landlock's network rules match the port alone, so the command can reach that port on any host and send a secret there. What was weighed, for servers without root on Ubuntu 24.04 and Debian:

- A network namespace with only loopback (and the proxy reached through a socket passed in) is the clean fix, but needs an unprivileged user namespace; Ubuntu 24.04 denies its capabilities by AppArmor (`kernel.apparmor_restrict_unprivileged_userns=1`; `unshare -rn` fails with "write failed /proc/self/uid_map"). Usable where user namespaces are allowed (Debian, or a profile for the binary), so it could be an opt-in later.
- A Unix socket to the proxy, with seccomp's AF_UNIX ban lifted for that one socket: seccomp cannot see the path, and the tools a sealed command runs (git, curl, npm, ssh's ProxyCommand aside) take an `http(s)://host:port` proxy, not a socket.
- seccomp user notification on `connect()`: the server would read each address from the command's memory (racy: another thread can change it after the check), or connect a socket to the proxy itself and put it in place of the command's (`SECCOMP_IOCTL_NOTIF_ADDFD` with `SETFD`). The second is sound but drops the command's socket options (non-blocking, keepalive), needs the listener fd handed from the helper to the server, and a supervisor loop per command: a larger change than the rest of the sandbox.
- No connect at all, with the proxy reached through a pre-opened fd: same problem as the socket, tools cannot use an fd for their proxy.

So the gap stays, documented; ADDFD is the candidate when it is taken on. Done meanwhile: the scratch folder (HOME, TMPDIR, the SSH key) is in `/tmp/xhaos-<uid>/sealed/`, outside every other command's reach; only harmless devices are open (no `/dev/shm`, no terminals); listening is refused (Landlock's bind rule, autobind on `listen()` included); UDP is refused (seccomp). `.git` stays writable, because push and fetch write refs and objects there; anything a sealed command writes there can be read by later commands, so it is a channel too, accepted: approve each sealed command knowing that.
