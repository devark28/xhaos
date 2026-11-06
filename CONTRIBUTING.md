# Contributing to Xhaos

Thanks for your interest. Bug reports, fixes, documentation and features are all welcome. For anything larger than a small fix, please open an issue first to agree on the approach; it saves you from building something that does not fit.

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE), and to follow the [code of conduct](CODE_OF_CONDUCT.md).

## Setting up

You need [Go 1.26+](https://go.dev/dl/), [Node.js 22.18+](https://nodejs.org), `git`, and Linux for the command sandbox (macOS works without it).

```sh
git clone https://github.com/devark28/xhaos.git
cd xhaos
npm ci
scripts/server.sh -root "$PWD"     # the server on http://localhost:5180, with this repo as a project
```

`scripts/server.sh` builds the page and runs the server with `go run`. It serves `frontend/` from disk, so a page change needs only `npm run build` and a reload; a Go change needs a restart. Flags: `-root DIR`, `-db PATH` (use a throwaway database while developing: `-db /tmp/xhaos-dev.db`), `-frontend DIR`, `-sandbox auto|on|off`, `-public-host`, `-trusted-proxy`, `-verbose`; the port is `WAILS_SERVER_PORT` (default 5180).

In development the server serves `frontend/` from disk, so frontend edits need only a reload; release binaries serve the page built into them. When connected, the page shows a toast, sets `<html data-backend="…">`, and exposes `window.xhaos` (for example `await xhaos.call('agent', 'Snapshot', 'default')`). Cards show the backend's agents and transcripts, and creating agents and sending messages go to the server. The prototype's sample agents are hidden by default; `/samples on` mixes them in (renumbered from #9000 and marked `· sample`, with actions on them staying in the browser) and `/samples off` hides them again.

### The page alone

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

Without a server, `npm run dev` runs the page as a prototype on mock data. Task data persists in browser local storage, not on a server. Clear the `xhaos-v1` local-storage entry to restore seed data; workspace memory notes use `xhaos-memory-v1`. The demo contains no credentials or real integrations. MCP, agent execution, remote connections, Wails integration, and automatic approval classification are not implemented.

## Tests

```sh
CGO_ENABLED=0 go test ./app/...                 # the services
CGO_ENABLED=0 go test -tags server ./cmd/xhaos  # the server wiring (includes the workspace check of every bound method)
CGO_ENABLED=1 go test -race ./app/...           # with the race detector (needs a C compiler)
npm run check && npm test                       # typecheck, build, and the frontend unit tests
```

Please run these before sending a change, and add tests with it: a Go test beside the code for server behaviour, a `*.test.ts` beside a logic module for the page. Tests must not touch your real data: use a temporary database (`t.TempDir()`), never `~/.local/share/xhaos`.

## How the code is organized

Requires Go 1.26+. The backend is Go: `cmd/xhaos` is the Wails headless host and `app/` holds transport-independent feature packages. `frontend/src/server-client.ts` is the browser-side client; under `npm run dev` it finds no Wails runtime and the prototype keeps using mock state.

See [docs/architecture.md](docs/architecture.md) for the package map, and [docs/design-notes.md](docs/design-notes.md) for the reasoning behind the design. A few rules the code follows:

- **Feature packages.** Each package under `app/` owns its types, rules and tables. Shared pieces (`apperr`, `eventlog`, `idempotency`, `sqlitedb`) are reused, not copied. Only `cmd/xhaos` imports Wails.
- **Every service method names its workspace.** A method the page can call takes the workspace ID as an argument or a `workspaceId` field; the server checks it against the session before running the call, and a test fails for any method that does not. A truly server-wide method goes on the test's explicit list, with a reason.
- **The server decides.** Permissions, policy, path confinement and secrets are enforced on the server. The page only shows and asks.
- **Pure Go.** No cgo, so the binary stays static.
- **Frontend logic stays testable.** Framework-free logic goes in `frontend/src/*.ts` with unit tests; components in `frontend/src/ui/` stay thin.

## Sending a change

1. Fork the repository and create a branch from `main`.
2. Keep the change focused; one topic per pull request.
3. Write commit messages as a short, imperative subject in the [Conventional Commits](https://www.conventionalcommits.org) style the history uses (`feat:`, `fix(ui):`, `docs:`), with a few lines of body if the why is not obvious.
4. Update the documentation (`README.md`, `docs/`) when behaviour changes.
5. Open a pull request describing what changed and how you tested it. Screenshots help for UI changes; please check both the dark and light themes, a phone-sized window, and Firefox as well as Chrome.

## Reporting bugs

Open an issue with what you did, what you expected, what happened, and your OS, kernel (`uname -r`), browser, and Xhaos version (shown in the page's toast and at `/health`). Server logs help; run with `-verbose` for more. Remove API keys, secrets and private paths before posting.

Security problems are different: please report them privately as described in [SECURITY.md](SECURITY.md).
