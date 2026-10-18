# Working on Wok

Wok is a Go CLI that attaches directly to persistent Chromium over CDP.
A managed profile owns one browser plus an optional local HTTP viewer process.
Read [README.md](README.md) for setup and [docs/agents.md](docs/agents.md) for
agent usage. The bundled reusable skill lives at [skills/wok/SKILL.md](skills/wok/SKILL.md).

## Repository map

- `cmd/wok`: executable entry point and internal render-process entry point.
- `internal/flow`: strict YAML/JSON batches, assertions and failure reports.
- `internal/cli`: command parsing, profile routing, JSON stdout/stderr.
- `internal/session`: private state, locking, process lifecycle and discovery.
- `internal/cdp`: WebSocket request/response/event transport.
- `internal/browser`: page actions, accessibility snapshots and persistent refs.
- `internal/provision`: official headless-shell download, cache and installation lock.
- `internal/render`: loopback HTTP viewer and browser control endpoints.
- `skills`: embedded agent skill and its install/show/list commands.
- `integration`: real CLI subprocess tests using a local fixture.
- `.github/workflows`: CI matrix and tag-triggered release; `install.sh`/`install.ps1` install releases.
- `bench`: development-only Playwright benchmark and viewer smoke test.

## Checks

```sh
go test -race ./...
go vet ./...
go build -o bin/wok ./cmd/wok
WOK_TEST_BROWSER=/absolute/path/to/chrome-headless-shell go test -race ./... -count=1
npm ci --prefix bench
WOK_BENCH_BROWSER=/absolute/path/to/chrome-headless-shell node bench/viewer-smoke.mjs
```

Use Go 1.23+. Wok supports Linux, macOS and Windows; keep OS-specific lifecycle
code in tagged files (`*_unix.go`, `*_windows.go`). Browser binaries and npm dependencies stay outside Git. Playwright is
only a development dependency. New runtime dependencies require a concrete
reason; prefer Go's standard library.

Keep profile isolation, detached browser lifetime, explicit external-browser
closure, and stale-ref rejection intact. Never report shutdown confirmed solely
because a probe timed out. A command ending must not close the browser. Explicit
`--tab` must never silently pick another tab. Test state changes across separate
CLI processes, rather than only through an in-process client.

Keep authored product material branded as Wok. Benchmark claims must distinguish
new Node processes, warm CDP attachment, and resident Playwright. Update the
agent guide and bundled skill when user-facing command behavior changes.
