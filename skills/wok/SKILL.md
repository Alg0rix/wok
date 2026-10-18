---
name: wok
description: Browse and interact with websites using the Wok CLI over CDP. Use for browser automation, page inspection, form filling, screenshots, and live browser viewing with persistent isolated profiles.
---

# Wok

Wok is a lightweight CLI that reconnects to a running CDP browser. Start once per profile; later commands reuse its browser, cookies, storage, and tabs. Multiple profiles can run concurrently. The `wok` binary must be on PATH. If no supported browser is available, `start` automatically downloads Chromium headless shell to a shared cache; subsequent starts reuse it offline. Use `wok browser install` to preinstall it.

## Start and reuse

Choose a profile for the task and include the same `--profile NAME` on every local browser command. Check status before starting; reuse an existing browser when appropriate.

```sh
wok --profile research status
wok --profile research start
wok --profile research open https://example.com
wok --profile research snapshot -i
```

A stopped profile's status fails; start it if the task needs a new session. `start` is idempotent for a running profile. Browser sandboxing is enabled by default; do not add `--no-sandbox` to ordinary commands. To choose an executable, use `wok --profile research start --browser /path/to/chrome-headless-shell`.

## Inspect and interact

Use `snapshot -i` to discover interactive elements. The snapshot returns actual `@eN` references; use the values from that output. For example, **only if the current snapshot contains these refs**:

```sh
wok --profile research fill @e4 'search terms'
wok --profile research click @e7
wok --profile research snapshot -i
```

Refresh the snapshot after navigation or significant page updates and when a ref becomes stale. Each snapshot invalidates previous refs. Refs are tied to the tab and document; never copy refs between profiles or tabs. CSS selectors also work, but must match exactly one element. Click rejects covered or disabled elements; fill replaces an editable element's contents.

```sh
wok --profile research fill 'input[name="q"]' 'search terms'
wok --profile research press Enter
wok --profile research wait --selector '#results'
wok --profile research get text
wok --profile research get text '#results'
wok --profile research get attr 'a.result' href
wok --profile research eval 'document.title'
wok --profile research screenshot page.png
```

`wait --selector` waits for existence, not visibility. `eval` supports promises. `screenshot` captures the current viewport as PNG. CSS lookup and element actions address the main document; iframe traversal is unsupported. Use raw CDP when the task requires capabilities beyond the convenience commands.

Most successful commands emit JSON. Snapshot defaults to readable rows; add `--json` for structured nodes:

```sh
wok --profile research --json snapshot -i
```

Failures return a nonzero exit status and a JSON error on stderr. Check the exit status before consuming output or continuing a dependent action. An absent or ambiguous selector, stale ref, or disconnected browser requires inspecting the current page or session before retrying.

## Live view and external browsers

`wok --profile research status` includes `renderUrl`, the local viewer base URL. Append `/render` to open its live view rather than guessing the port. If the viewer is not running, use `wok --profile research render` and inspect its JSON output. The viewer supports browser interaction; keep access to this control surface within the task's authorized environment.

For an existing externally managed browser, explicitly pass `--cdp` on every command instead of relying on a local profile. HTTP discovery endpoints and browser WebSocket endpoints are accepted:

```sh
wok --cdp http://127.0.0.1:9222 status
wok --cdp http://127.0.0.1:9222 open https://example.com
wok --cdp http://127.0.0.1:9222 snapshot -i
```

Commands detach when finished and leave the browser running. Stop a profile only if this task owns its browser lifecycle:

```sh
wok --profile research stop
```

Stopping preserves profile data (`delete-data` wipes it). Do not close a shared external browser as routine cleanup.

For tab targeting (`tab-list`, `tab-select`), extended actions (`dblclick`, `hover`, `drag`, `drop`, `select`, `check`, `uncheck`), raw keyboard/mouse (`keydown`, `keyup`, `mousemove`, `mousedown`, `mouseup`, `mousewheel`), storage state (`state-save`, `state-load`, `cookie-*`, `localstorage-*`, `sessionstorage-*`), network routing (`requests`, `request`, `route`), DevTools (`recording-start`, `tracing-start`, `video-start`, `pause-at`, `generate-locator`, `highlight`), and raw CDP, read [references/commands.md](references/commands.md).
