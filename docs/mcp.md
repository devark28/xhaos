# Driving Xhaos from Claude Code (MCP)

Xhaos is an MCP server as well as an MCP client. An MCP client such as Claude Code connects to it over HTTPS and can list and start agents, give them tasks, read and answer their questions, and talk with them in channels, for you, without you opening the page. It works wherever the server is reachable: your machine can be offline.

The endpoint is `https://<your domain>/mcp` (also served at `/xhaos/mcp`). It speaks MCP's Streamable HTTP transport, statelessly: every request stands alone, so a server restart or a dropped connection loses nothing on the client's side.

## Set it up

1. **Create a token.** On the page: Settings → **MCP access** → **Create a token**. Give it a name (it is shown beside everything the client does), tick the scopes it needs, and enter the workspace's passphrase. The token (`xhm_…`) is shown once, with the command below; only its SHA-256 hash is stored.
2. **Add it to Claude Code:**

   ```sh
   claude mcp add --transport http xhaos https://xhaos.example.com/mcp \
     --header "Authorization: Bearer xhm_…"
   ```

   Add `--scope user` to have it in every project, or `--scope local` (the default) for the current one. `claude mcp list` should show it as connected.
3. **Use it.** Ask Claude Code to "start an Xhaos agent on …", "check whether my agents need anything", or "answer agent #4's question". The server sends Claude Code short instructions on how the tools fit together.

A token reaches only the workspace it was created in. **Revoke** (same section) deletes it: requests with it fail at once. The section lists each token's scopes and when it was created and last used.

## Scopes

| Scope | What the client may do | Tools |
|---|---|---|
| `read` | List agents and their status, read their messages and tool calls, list pending questions and approvals, read channels, wait for events | `list_agents`, `get_agent`, `list_questions`, `wait_for_events`, `list_projects`, `list_models`, `list_channels`, `read_channel` |
| `drive` | Create top-level agents, send them messages and tasks, start (or bring back) and stop (or terminate) them, open a channel with an agent, post to channels | `create_agent`, `send_message`, `start_agent`, `stop_agent`, `open_channel`, `post_channel` |
| `answer` | Answer agents' questions (`ask_user`) for you, or dismiss them | `answer_question` |
| `approve-feature-branch` | Allow or deny an agent's git commit or push, only when it stays on branches that are not protected | `answer_question` |

A client sees only the tools its token's scopes allow. Give each token the least it needs: a monitoring token needs only `read`.

## The tools

- `list_agents`: number (`#N`), title, status (`idle`, `running`, `attention` = waiting for you, `paused`, `complete`, `terminated`), parent, project, model, and how many questions or approvals each waits on.
- `get_agent`: one agent's pending questions and approvals (every option included), its channels, and its recent messages and tool calls.
- `list_questions`: every pending question and approval across all agents, each with its agent's number and title and an id (`<agent>/<call id>`), and whether this token may answer it (and why not). A title an agent proposed for itself is listed too, as kind `rename` (id `<agent>/rename`, with `proposed_title` and `reason`); the agent does not wait for it.
- `wait_for_events`: holds the call (up to 50 s) until something happens (a reply, a status change, a question, an approval, a channel message) and returns it. Call it once without `since` to get the current position, then pass the `seq` it returns. A token may hold 4 waiting calls at once (`wait_for_events` and `read_channel` with a wait), a workspace 16; more return at once with `too many waits`.
- `list_projects`, `list_models`: the choices for `create_agent`, and the default model.
- `create_agent`: a task, and optionally a title, project, provider and model, and a policy (`access` rw/ro, `review` manual/auto, `commit` review/blocked). Whatever it leaves out comes from the workspace's default policy (Settings → Agents; read-write, manual, commits review unless the owner changed it). Commits `allowed` is never accepted here, and an existing agent's policy can only be changed by the owner on the page.
- `send_message`, `start_agent`, `stop_agent`: as the page's composer, Continue, Stop and Terminate.
- `list_channels`, `read_channel` (after a message id, optionally waiting up to 50 s), `post_channel`, `open_channel` (a channel with one agent, which answers there with `channel_send`).
- `answer_question`: an `ask_user` question by option labels (or ids) and/or text, per question (`answers`) or with `choice`/`text` when there is one question, or `dismiss`; a commit or push with `decision` `allow` or `deny`; a `rename` with `decision` `rename` or `keep` (scope `answer`; the agent's history names the token).

## What stays the owner's

- **The lock.** After a restart the server is locked until you sign in on the page; until then every tool answers `server locked: the owner must sign in…`. A token does not unlock anything: agents wait for you, as they always do.
- **Approvals other than commits.** Commands, file changes, web access, secrets, channels to other agents and subagent spawns are approved only on the page. `list_questions` shows them (so the client can tell you) with `you_can_answer: false`.
- **Commits and pushes beyond feature branches.** `approve-feature-branch` never approves the agent's command text. Xhaos reads the waiting command only to find what the agent wants, and runs its own command for that in the agent's place (the agent is told, and the card shows what ran):
  - **commit**: the command must be `git add …` and `git commit -m …` (optionally after `cd` into a folder of the project; `-a`, `-s` and `-q` allowed), with no shell metacharacters or globs (`` ` $ < > \ * ? [ ] ~ ^ { } @{ ``). HEAD must resolve, through every symbolic ref, to a branch that is not protected. Xhaos runs `git add` and `git commit --no-verify` with hooks and fsmonitor off and the message as quoted text, after checking that HEAD still resolves to that branch.
  - **push**: the command must be `git push [-u] [<remote>] [<branch>]`. The remote must be one configured in the repo by name, with one https or ssh URL, no `pushurl`, `receivepack` or proxy, no `insteadOf` or `pushInsteadOf` that rewrites it, and no `core.sshCommand` or `core.gitProxy` in the repo. The branch must be an existing local branch (not a symbolic ref) that is not protected. Xhaos then pushes `refs/heads/<branch>:refs/heads/<branch>` to the remote's URL itself, with `--no-follow-tags`, hooks off, only that URL's transport allowed, and the global and system git config left out (so the remote's `push` and `mirror` settings and any credential helper in your global config do not apply: credentials must come from a secret, such as an SSH key, or the remote's URL).
  - Protected (compared without case): `main`, `master`, `trunk`, `develop`, `production`, `integration`, `release…`. Tags, wildcards, refspecs, force and deletion cannot come out of this. Anything else, including a command using secrets you have not trusted to the agent, is yours to decide; `list_questions` says why, and for what it can allow, shows the intent (`intent`) and Xhaos's command (`will_run`). Denying is always allowed with this scope.
- **Policies.** `create_agent` cannot choose `commit: allowed` (commits without review); dangerously allow, secrets, grants, providers, projects, MCP servers, nodes and tokens are managed only on the page.

## Attribution

Everything a token does is recorded as yours with the token's name beside it: a task or message shows **via MCP · <name>** on the card, an answered question says *answered via MCP (<name>)*, an allowed commit *allowed via MCP (<name>)*, stopping or starting an agent leaves a note in its transcript, and channel messages show *<name> (MCP)*. Agents receive channel messages from a token marked as coming from an MCP client acting for you.

## Without Claude Code

Any MCP client works. With curl (the `Accept` header is required by the transport):

```sh
curl -s https://xhaos.example.com/mcp \
  -H 'Authorization: Bearer xhm_…' -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_questions","arguments":{}}}'
```

A missing, wrong or revoked token gets 401; wrong tokens are counted per address and slowed down like wrong passphrases (429 with `Retry-After`). A valid token is always served, so guesses from one address never lock out working clients. Behind a reverse proxy, start Xhaos with `-trusted-proxy 127.0.0.1,::1` (the installer does when you give it a domain) so the slow-down counts each client's own address rather than the proxy's.
