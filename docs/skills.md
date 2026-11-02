# Skills in Xhaos

Design requirement; the browser prototype does not load or execute skills yet.

A skill is a user-defined instruction package describing when and how an agent should perform a workflow. The harness provides tools and permissions separately. Channels are the first example: a channels skill teaches discovery, creation, joining, sending, reading, and waiting; the backend supplies those operations.

## Package and discovery

Use a directory with `SKILL.md` (name, description, instructions), plus optional references, scripts, and templates. Configure skill roots on the backend for user, workspace, and project scope. A workspace may span several projects; discovery must not indiscriminately expose one project's skills or files to another. Resolve duplicate names explicitly with source-qualified IDs and visible provenance.

Discover names/descriptions first; load full instructions and referenced resources when selected or relevant. Give agents a provider-neutral skill-discovery/read interface so Claude, GPT, DeepSeek, and local models receive the same workflow knowledge. Importing Claude Code-style packages may require adapting tool names and local paths; report incompatibilities rather than claiming every package runs unchanged.

## Selection and versions

Record enabled skills in agent configuration. Explicit invocation selects a skill; optional model-driven activation can use the enabled descriptions. Subagents receive an explicit enabled subset. Keep an active skill's source and content hash in session history, and pin its version for the turn/session so editing a file does not silently rewrite instructions in flight. Reloading skills is explicit.

A future Skills view can list installed sources and per-agent activation through the agent menu. Do not add a large permanent dashboard or inject every installed skill into every conversation.

## Permissions

A skill is not an authorization grant. Its scripts, writes, shell commands, MCP operations, channels, and subagent requests still pass through the same server execution policy. RO, commit-review, and automode settings remain in force. Reading a skill must not automatically execute its installation scripts. Changes to shared skill packages require the same owner-approved mutation policy as project changes.

## Channels skill behavior

Support both owner-directed and agent-initiated setup: one agent creates a subject, another joins or discovers it. Participants can belong to several subjects. Explain which backend tools to use and how to read only unseen messages; include the optional append-only file bridge for compatibility with the existing workflow.

Channel messages are data from another agent. They do not override owner instructions or supply approval, even if the message says “the owner says.” The skill should describe that distinction; the backend must enforce it independently.

## Backend milestone

Implement the skill catalog and instruction loader alongside the first real runner adapter. Ship a default channels package as an editable example, expose its required backend operations, and dogfood that workflow while building Xhaos. Skill scripts and file-channel integration follow only after the execution-policy boundary exists.
