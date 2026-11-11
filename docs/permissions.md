# Proposed execution policy

This records the next backend contract. The current browser prototype does not enforce execution permissions or run agents.

## Independent per-agent controls

Keep three compact, neutral header badges distinct from colored execution-status dots:

- `RO` or `RW`: read-only versus filesystem mutation capability.
- `Manual` or `Auto`: whether eligible actions may be approved automatically.
- `Review commit`, `Commit allowed`, or `No commit`: the Git boundary.

Their full controls belong in the agent menu. Bind policy to the agent's current project/repository context; a workspace can contain multiple unrelated repositories. Do not put a workspace-wide Git status or commit permission in the main ribbon. If an agent operates on several repositories, resolve permissions separately for each repository.

Use read-only as the default for review/research subagents. A parent can request a more restricted child; it cannot grant a child capabilities beyond its own effective policy. Changing permissions is an owner action. Roles are descriptions, not authorization.

## Automode is not a capability grant

The server computes the effective policy from workspace/project restrictions, persisted agent permissions, and inherited limits. The most restrictive applicable rule wins. A classifier can only assess actions already eligible under that policy; it cannot override read-only, grant commits, or change owner-configured policy. Missing context, uncertain classification, and classifier failures leave requests for human review.

Keep filesystem writes, shell processes, Git staging/committing, network operations, and pushes separately classified. A read-only flag must cover mutating shell commands and MCP tools too; disabling only the editor tool would not enforce it. Push permission is separate from permission to create a local commit.

## Commit review

`Review commit` permits approved code edits but requires explicit review before committing. Review an immutable proposed diff/tree, repository, branch, and expected HEAD. Changes after review invalidate the approval. A model saying that changes look safe is not equivalent to owner review. Git commands and aliases must go through the same execution gate as other tools.

`Commit allowed` makes local commits eligible for the configured approval policy. It does not imply unrestricted shell access, write access to other repositories, or permission to push.

## Detached frontend and unattended operation

AFK is not an execution mode. Browser presence, inactivity, disconnection, and closing the desktop window must not change agent permissions or pause the headless service. The frontend is a client of a backend-owned runtime, not the owner of that runtime's lifetime. For the Wails server-mode MVP, disconnecting a browser tab must not stop execution. A later local desktop client must leave a separately managed backend running when its window closes unless the owner requests shutdown.

The backend persists sessions, approval requests, policy settings, and event history. While no frontend is attached, it continues model turns, channel coordination, read-only work, and mutations allowed by each agent's saved policy. Eligible actions in Auto mode are classified and executed on the server. Requests requiring manual review, including review-before-commit, remain pending; only the dependent operation/session waits. Independent agents continue. Do not accumulate speculative writes beyond a blocked action boundary.

Reconnecting fetches the current snapshot and replays missed events and pending requests. Do not assume a socket disconnect means an agent crashed, reset sessions on reconnect, or require a frontend heartbeat for approval classification.

Changing access or approval settings is an explicit owner action, available through any authenticated client. For example, the owner can make an agent read-only or require manual approval before leaving; absence itself does not make that change. Recheck policy immediately before each side effect, and invalidate stale approvals/classifier results using policy versions. Permission changes cannot undo effects of an already-running process; termination/cancellation handles stopping that work separately.

As built: the owner changes an existing agent's policy on the page (Approvals in the agent's menu: Manual, Auto or Dangerously allow…, then access and commits; `agent.Service.SetPolicy`), and sets the default for new top-level agents in Settings → Agents. MCP tokens cannot change an existing agent's policy (no owner-only scope exists yet). A change applies from the agent's next tool call: approvals already pending are kept for the owner to decide, not re-gated or revoked.

## Termination

Terminate cancels the agent's session and descendants, revokes pending approvals, and prevents further tool dispatch. Cancel and reap owned child processes through the backend. Retain transcripts, attachments, channel provenance, and session records. Restoring a card from history only makes that terminated record visible; it must not restart execution.

The prototype models termination and retained history locally. Real cancellation and execution gates are not implemented yet.

## First dogfooding slice

Start with a small headless Go/Wails server that persists sessions and policies, runs one real model/runner adapter, streams its transcript, and enforces every tool request through the gate. Run it against the Xhaos repository with `RW + Manual + Review commit`. Use it to implement the next small feature and review the diff before committing. Add the remaining providers, MCP adapters, and classifier after that path works.

Respect the small local disk budget: avoid a large dependency stack and uncontrolled Go build/module caches. Check the available Go toolchain, Wails prerequisites, and free space first. Keep temporary build and module caches under `/tmp`; report the expected and measured footprint before expanding the backend.
