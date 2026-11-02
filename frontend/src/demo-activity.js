import { demoQuestions } from './questions.js';
// Preview fixtures only. Commands and outputs below are examples, never executed.
const samples = {
  11: ['Build the workspace foundation', [
    ['user', 'Build the workspace foundation. Keep the browser client separate from execution so we can connect the same UI to a local server or a VPS.'],
    ['assistant', 'I’ll start with the state boundary and keep process execution out of the client. Agent #14 is handling persistence; #15 will review the isolation rules.'],
    ['tool', 'Inspect existing frontend files', 'rg --files frontend/src', 'frontend/src/app.js\nfrontend/src/model.js\nfrontend/src/style.css'],
    ['assistant', 'The current state is owned by the browser. I’m extracting a store interface so the UI can consume snapshots and events later without depending on how they are delivered.'],
    ['diff', 'Proposed store interface', 'frontend/src/store.js', '+ export function createStore(transport) {\n+   return {\n+     snapshot: () => transport.snapshot(),\n+     subscribe: handler => transport.subscribe(handler),\n+     dispatch: command => transport.dispatch(command),\n+   };\n+ }'],
    ['tool', 'Check JavaScript syntax', 'node --check frontend/src/store.js', 'Exit 0 · no syntax errors'],
    ['assistant', 'Wiring workspace state into the application shell. Next I’ll connect the event subscription and check that switching contexts cannot replay another context’s events.']
  ]],
  14: ['Implement workspace persistence', [
    ['user', 'Persist tasks without mixing data between workspace contexts. Handle unavailable or corrupt browser storage.'],
    ['assistant', 'I’ll put serialization behind a small storage adapter and treat saved data as untrusted input. The in-memory state should still work when storage is unavailable.'],
    ['tool', 'Read the current persistence code', 'rg -n "localStorage|JSON.parse" frontend/src', 'app.js:5: JSON.parse(localStorage.getItem("xhaos-v1"))\napp.js:8: localStorage.setItem("xhaos-v1", JSON.stringify(workspaces))'],
    ['diff', 'Proposed fallback on invalid data', 'frontend/src/storage.js', '+ try {\n+   const saved = JSON.parse(storage.getItem(key));\n+   return validateSnapshot(saved) ? saved : seed();\n+ } catch {\n+   return seed();\n+ }'],
    ['tool', 'Run storage boundary tests', 'node --test frontend/src/storage.test.js', '✓ restores valid snapshots\n✓ falls back after malformed JSON\n✓ isolates workspace keys\n3 passed'],
    ['assistant', 'Adding workspace isolation and local storage. The remaining case is quota exhaustion: I’m keeping the update in memory and exposing a save failure instead of discarding the task.']
  ]],
  15: ['Review the storage boundary', [
    ['user', 'Review the persistence implementation for cross-workspace reads and writes.'],
    ['assistant', 'I’ll trace the key construction and restore path, then check that invalid saved records cannot redirect access to a different context.'],
    ['tool', 'Find storage entry points', 'rg -n "storage|workspaceId" frontend/src/storage.js', '12: const key = `xhaos:${workspaceId}:snapshot`;\n24: export function restore(workspaceId) {\n38: export function persist(workspaceId, snapshot) {'],
    ['assistant', 'The key boundary looks explicit, but restored agent records need an ownership check as well. A separate reviewer could inspect that path while I test the serialization fallback.'],
    ['approval', 'Subagent request', 'spawn_agent({ task: "Review restored agent ownership", scope: "read-only" })', 'Awaiting your approval. No subagent has been started.']
  ]],
  12: ['Explore the agent orchestration layer', [
    ['user', 'Design the connection between the browser client and a headless Rust process. It must work locally and on a remote machine.'],
    ['assistant', 'I’m comparing a persistent bidirectional connection with HTTP commands plus a resumable event stream. The key requirement is recovering after a disconnect without repeating a tool action.'],
    ['tool', 'Inspect protocol notes', 'cat docs/architecture.md', 'Commands carry idempotency keys.\nSnapshots include an event sequence.\nApproval refers to a specific pending request.'],
    ['assistant', 'Agent #16 recommends HTTP commands plus server-sent events for the first version. Both approaches still need durable request IDs and an event cursor.'],
    ['diff', 'Proposed event envelope', 'crates/protocol/src/event.rs', '+ struct AgentEvent {\n+     workspace_id: WorkspaceId,\n+     agent_id: AgentId,\n+     sequence: u64,\n+     payload: EventPayload,\n+ }'],
    ['approval', 'Permission to run a transport experiment', 'cargo run -p transport-spike -- --bind 127.0.0.1:8787', 'Would compile and start an experimental local server. Waiting for approval; this preview does not run it.']
  ]],
  16: ['Compare event transport options', [
    ['user', 'Compare transports with reconnect behavior and headless operation in mind.'],
    ['assistant', 'The client mostly sends discrete commands and receives a stream of state changes. I’ll evaluate recovery semantics before implementation complexity.'],
    ['tool', 'Inspect the proposed command set', 'rg -n "Commands|event|reconnect" docs/architecture.md', 'create task · send message · pause · resume\napprove request · decline request\nresume from the last committed event sequence'],
    ['assistant', 'Recommendation: HTTP commands plus an SSE stream for the initial protocol. Use an idempotency key for commands and a monotonic sequence for events. WebSocket remains an option if interactive terminal sessions become a requirement.'],
    ['assistant', 'Completed the comparison. A transport alone does not prevent duplicated tool execution: the server must record accepted commands before dispatching side effects. Sent these findings to agent #12.']
  ]],
  13: ['Design a calmer command center', [
    ['user', 'Make the interface dense. Keep task text visible, use neutral surfaces, and show status through small colored dots.'],
    ['assistant', 'I’ll remove the summary dashboard and use the top quarter for input. The remaining area will hold independent agent columns with collapsed rows by default.'],
    ['tool', 'Find layout rules', 'rg -n "agent-grid|composer|ribbon" frontend/src/style.css', 'agent-grid: two columns\ncomposer: top command area\nribbon: compact controls, no bottom separator'],
    ['diff', 'Independent column layout', 'frontend/src/style.css', '- grid-auto-rows: auto;\n+ .agent-column {\n+   display: flex;\n+   flex-direction: column;\n+   gap: 6px;\n+ }'],
    ['assistant', 'I’ve asked #17 to inspect keyboard behavior and #18 to investigate compact alternatives. Expanded cards should show the conversation first, with tool output disclosed inline.'],
    ['assistant', 'Refining agent hierarchy and attention states. I’m checking long titles, multiline output, and how neighboring cards move during expansion.']
  ]],
  17: ['Audit keyboard navigation', [
    ['user', 'Check navigation without a mouse, including the slash completion menu.'],
    ['assistant', 'I’ll trace the order from the ribbon to the composer, command results, and agent disclosure controls.'],
    ['tool', 'Inspect focus and keyboard handlers', 'rg -n "keydown|aria-expanded|focus" frontend/src/app.js', 'Enter submits; Shift+Enter inserts a line\nArrowDown enters completion results\nEscape dismisses the menu\nAgent headers expose aria-expanded'],
    ['assistant', 'The menu supports navigation, but focus must return to the input after selection. Expansion controls also need to retain focus when the view updates.'],
    ['diff', 'Suggested focus restoration', 'frontend/src/app.js', '+ const targetId = document.activeElement?.dataset.collapse;\n+ render();\n+ document.querySelector(`[data-collapse="${targetId}"]`)?.focus();'],
    ['assistant', 'Checking focus order and disclosure controls. Next pass: reduced-motion behavior for attention dots and readable tool output at narrow widths.']
  ]],
  18: ['Explore compact card layouts', [
    ['user', 'Explore layouts that keep expanded conversations readable without wasting horizontal space.'],
    ['assistant', 'I’m comparing one full-width transcript with two independent columns. A shared grid row creates a gap when only one neighboring card expands.'],
    ['tool', 'Inspect current card sizing', 'rg -n "align-items|grid-template-columns" frontend/src/style.css', 'grid-template-columns: repeat(2, minmax(0, 1fr));\nalign-items: start;'],
    ['assistant', 'Aligning cards to the start does not remove the shared row height. Independent stacks avoid that coupling while keeping each card in its assigned column.'],
    ['approval', 'Subagent request', 'spawn_agent({ task: "Compare narrow-screen transcript layouts", scope: "design exploration" })', 'Waiting for your decision. The proposed subagent has not started.']
  ]],
  19: ['Map MCP integration boundaries', [
    ['user', 'Plan MCP integration without putting tool credentials in the frontend.'],
    ['assistant', 'The Rust server should own discovery, connection lifecycle, and tool execution. The client only needs tool descriptions and permission requests.'],
    ['tool', 'Example MCP tool discovery', 'mcp.list_tools({ server: "filesystem" })', '{ "tools": [\n  { "name": "read_file", "inputSchema": { "path": "string" } },\n  { "name": "list_directory", "inputSchema": { "path": "string" } }\n] }'],
    ['assistant', 'A model tool call should resolve to a server-owned request with an immutable parameter snapshot. Approval applies to that request, not to every later action by the agent.'],
    ['tool', 'Example read-only tool call', 'mcp.call_tool({ server: "filesystem", name: "list_directory", arguments: { path: "/project" } })', 'docs/\nfrontend/\nscripts/\nREADME.md'],
    ['assistant', 'Paused while the orchestration contract is settled. No MCP server is connected in this prototype.']
  ]],
  20: ['Document the local development flow', [
    ['user', 'Write a short development guide that avoids a Rust build until the UI is ready.'],
    ['assistant', 'I’ll document the dependency-free preview, checks, and the distinction between mock state and agent execution.'],
    ['tool', 'Inspect development scripts', 'cat package.json', '{\n  "dev": "node scripts/dev.mjs",\n  "check": "node --check frontend/src/app.js && node --check scripts/dev.mjs",\n  "test": "node --test frontend/src/model.test.js"\n}'],
    ['diff', 'Development guide excerpt', 'README.md', '+ npm run dev\n+ # Open http://localhost:5173\n+ npm run check\n+ npm test\n+ # No Rust build or package installation required.'],
    ['tool', 'Example validation output', 'npm run check && npm test', 'Syntax checks passed.\n3 state-transition tests passed.'],
    ['assistant', 'Documentation is ready. It covers local persistence, provider selection, approvals, slash commands, and the future client/server boundary. Commands in this sample transcript are illustrative.']
  ]]
};
export function hydrateDemoActivity(workspaces) {
  const visit = agents => agents.forEach(agent => {
    const sample = samples[agent.id];
    if (sample?.[0] === agent.title) agent.demo = true;
    if(agent.demo&&agent.id===12&&agent.status==='waiting'&&!agent.questionBatch){agent.questionBatch=demoQuestions();}
    if (!agent.activity && sample?.[0] === agent.title) {
      agent.activity = sample[1].map(([kind, text, command, output]) => ({ kind, text, command, output }));
    }
    visit(agent.children || []);
  });
  workspaces.forEach(workspace => visit(workspace.agents));
  return workspaces;
}
