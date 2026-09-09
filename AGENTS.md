# Repository Guidelines

## Project Structure & Module Organization

- `apps/web/` contains the React 19 + TypeScript frontend. Organize code by responsibility under `src/app`, `components`, `entities`, `features`, `hooks`, `pages`, `services`, `stores`, and `utils`.
- `apps/manager-server/` is the Go 1.24 backend. Entry points live in `cmd/`; internal packages cover HTTP APIs, repositories, services, storage, workers, and security.
- `apps/docs/` is the VitePress documentation site. Keep English content under `apps/docs/en/` and matching localized pages in the parallel topic directories.
- `tests/` holds repository-level Vitest checks for installers, native control scripts, source integrity, and frontend architecture boundaries. Images and release tooling live in `img/` and `bin/`.

## Build, Test, and Development Commands

Run `npm ci` from the repository root for reproducible dependencies.

- `npm run dev` starts the web app with Vite.
- `npm run type-check && npm run lint` validates strict TypeScript and ESLint rules.
- `npm run test` runs co-located frontend tests plus root-level tests.
- `npm run build` creates the single-file panel at `apps/web/dist/index.html`.
- `npm run docs:dev` / `npm run docs:build` serve or build VitePress docs.
- `npm run check:demo-isolation` verifies production bundles exclude demo fixtures.
- `npm run manager-server:test` runs `go test ./...`; for backend changes also run `go test -race ./...` and `go vet ./...` from `apps/manager-server`.
- `docker compose -f docker-compose.manager.yml up --build` exercises the local stack.

## Coding Style & Naming Conventions

Prettier uses 2 spaces, single quotes, semicolons, trailing ES5 commas, and a 100-column width. Run `npm run format` for web source. Use PascalCase for React components and pages, `useX` for hooks/stores, and camelCase for utilities. Prefer the `@/` import alias. Keep `features` and `components` independent of `pages`; an architecture test enforces this. Format Go with `gofmt` and follow standard Go package naming.

## Testing Guidelines

Vitest files use `*.test.ts`, `*.test.tsx`, or root-level `*.test.mjs` and should sit beside the behavior they cover when practical. Go tests use `*_test.go` with `TestXxx` names. Add focused regression tests for bug fixes; no numeric coverage threshold is enforced.

## Commit & Pull Request Guidelines

Recent commits use concise, imperative subjects, typically `type(scope): summary`, such as
`feat(web): support ...`, `fix(manager-server): prevent ...`, or `docs(readme): clarify ...`.
Optional leading icons appear in history; keep each commit scoped to one concern.

Use the PR template: summarize intent, mark affected runtimes, document user/security impact and rollback risk, link issues, and list exact verification commands. Include screenshots or recordings for visible UI changes and update docs or release notes when behavior changes.

## Repository Remote Workflow

- Treat `https://github.com/seakee/CPA-Manager-Plus` as the read-only upstream. Fetch updates from `upstream` and rebase local work onto `upstream/main`.
- Treat `https://github.com/githubshansheng/CPA-Manager-Plus` as the writable fork. After completing and verifying a requested code change, commit only the files belonging to that task and push the commit to `origin` unless the user says not to.
- Never push commits to `upstream`. The local `main` branch pulls from `upstream/main` and pushes to `origin/main`.
