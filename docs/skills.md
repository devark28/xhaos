# Skills in Xhaos

A skill is instructions (and optional files: references, templates, scripts) for one kind of work, which an agent loads when a task calls for it. Xhaos uses Claude Code's format: a folder with a `SKILL.md` whose front matter gives the skill's `name` (lowercase letters, digits, hyphens) and `description`:

```markdown
---
name: release-notes
description: Write release notes from the git log, grouped by area.
---
# Release notes
Run scripts/collect.sh first, then …
```

## Where skills come from

- **Workspace skills** are uploaded in Settings → Skills as one `.skill` file: a zip of that folder (with `SKILL.md` at its top, or in its one top folder, as Claude's skill uploads are). They are kept beside the database (`skills/<workspace>/<name>.skill`) and read inside the zip, without unpacking. Uploading a skill with an existing name replaces it. At most 20 MB zipped, 60 MB unpacked, 1000 files; paths that leave the folder are refused.
- **Project skills** are folders in a project's `.xhaos/skills/` or `.claude/skills/` (so a repository's Claude Code skills work as they are). They are read where they are, for the agents working in that project only; another project's agents never see them. Links out of the folder are ignored. **Export .skill** zips one, to share or to upload as a workspace skill.

Each skill has an on/off switch. A project skill wins over a workspace skill of the same name for that project's agents.

## How agents use them

- An agent's system prompt lists the enabled skills it can use, by name and description only, so the prompt stays small and the cache warm.
- `skill_read(name)` returns the skill's `SKILL.md` and the list of its other files; `skill_read(name, file)` returns one of them (text, up to 100,000 characters). It is a read: it runs without asking.
- To run a skill's scripts, a `bash` call names the skill (`"skills": ["release-notes"]`) and uses `$SKILL_RELEASE_NOTES`, the skill's folder, which the sandbox makes readable but not writable for that command. A workspace skill is unpacked for this once per version (by content hash), so editing or replacing a skill never changes one in use mid-command.

## Permissions

A skill grants nothing. Its scripts run through `bash` like any command: the same policy (they ask under manual review), the same sandbox, the same secrets rules. Reading a skill never runs anything.

## Not yet

- Choosing skills per agent (today every agent sees every enabled skill it can reach); `/skill <name>` to attach one to a message.
- Agents proposing new skills for you to approve.
- The channels skill described in the original design: channels are tools today (`agents_list`, `channel_create`, …), documented in their descriptions.
