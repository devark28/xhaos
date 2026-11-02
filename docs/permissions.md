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

The server computes the effective policy from global holds, workspace/project restrictions, agent permissions, and inherited limits. The most restrictive applicable rule wins. A classifier can only assess actions already eligible under that policy; it cannot override read-only, grant commits, or release an AFK hold. Missing context, uncertain classification, and classifier failures leave requests for human review.

Keep filesystem writes, shell processes, Git staging/committing, network operations, and pushes separately classified. A read-only flag must cover mutating shell commands and MCP tools too; disabling only the editor tool would not enforce it. Push permission is separate from permission to create a local commit.

## Commit review

`Review commit` permits approved code edits but requires explicit review before committing. Review an immutable proposed diff/tree, repository, branch, and expected HEAD. Changes after review invalidate the approval. A model saying that changes look safe is not equivalent to owner review. Git commands and aliases must go through the same execution gate as other tools.

`Commit allowed` makes local commits eligible for the configured approval policy. It does not imply unrestricted shell access, write access to other repositories, or permission to push.

## AFK hold

Disabling automode alone is insufficient: already-approved writes could continue. Provide an owner-controlled hold on new mutations, per agent or across agents. It stops dispatching new writes, commits, and other mutations at the execution gate. Park the session at that boundary rather than generating a backlog of edits for later classification. Bounded read-only analysis may continue if explicitly allowed.

Already-running processes may have performed effects; a switch cannot undo them. Request cancellation where supported and show the remaining in-flight operation. Check policy again immediately before every side effect. Use policy versions so queued approvals and classifier results cannot execute after permissions change. Resuming the hold does not automatically approve pending work.

## Termination

Terminate cancels the agent's session and descendants, revokes pending approvals, and prevents further tool dispatch. Cancel and reap owned child processes through the backend. Retain transcripts, attachments, channel provenance, and session records. Restoring a card from history only makes that terminated record visible; it must not restart execution.

The prototype models termination and retained history locally. Real cancellation and execution gates are not implemented yet.

## First dogfooding slice

Start with a small headless Rust service that persists sessions and policies, runs one real model/runner adapter, streams its transcript, and enforces every tool request through the gate. Run it against the Xhaos repository with `RW + Manual + Review commit`. Use it to implement the next small feature and review the diff before committing. Add the remaining providers, MCP adapters, classifier, and Tauri shell after that path works.

Respect the small local disk budget: avoid a large dependency stack and uncontrolled Rust target caches. Choose and report the build/cache footprint before expanding the backend.
