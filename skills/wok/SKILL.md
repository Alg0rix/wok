---
name: wok
description: Browse and interact with websites using the Wok CLI over CDP. Use for browser automation, page inspection, form filling, screenshots, PDF export, network routing, storage state, tracing, video recording, debugging, and live browser viewing with persistent isolated profiles.
---

# Wok

Wok is a lightweight CLI that attaches directly to a persistent Chromium browser over CDP. Start once per profile (`wok -s=NAME start` or `wok -s=NAME open [url]`); subsequent commands reuse the same browser, cookies, storage, and tabs. Multiple profiles run concurrently in isolated browser processes. If no supported browser is installed, Wok automatically downloads Chromium headless shell to a shared cache on first start (or via `wok install-browser`).

## Start and reuse

Choose a profile name for the task and pass `--profile NAME` (or `-s=NAME`) on every command. Check status or list profiles before starting; reuse an existing browser when appropriate.

```sh
wok list
wok -s=research status
wok -s=research start
wok -s=research open https://example.com
wok -s=research snapshot -i
```

A stopped profile's `status` fails; start it with `start` or `open [url]` when the task needs a session. `start` is idempotent while healthy. Browser sandboxing is enabled by default; only pass `--no-sandbox` when required by a container environment.

## Inspect and interact

Use `snapshot -i` (or `snapshot -i TARGET` for a subtree) to discover interactive elements and their persistent `@eN` references:

```sh
wok -s=research snapshot -i
wok -s=research fill @e4 'search terms'
wok -s=research click @e7
wok -s=research wait --selector '#results'
wok -s=research get text '#results'
```

Refresh the snapshot after navigation or DOM updates that replace elements. Each snapshot invalidates previous refs. CSS selectors and `find` (`wok find [role|text|css] VALUE [--nth N]`) also work; CSS selectors must match a single element. `click` and `dblclick` scroll elements into view and reject detached, disabled, or covered elements.

## Complete command map

| Category | Commands |
|---|---|
| **Core** | `open [url]`, `goto <url>`, `attach [name\|url]`, `detach`, `start`, `stop`, `close`, `status`, `click <target> [button]`, `dblclick <target> [button]`, `fill <target> <text>`, `type <text>`, `drag <start> <end>`, `drop <target> <files...\|--text text>`, `hover <target>`, `select <target> <val...>`, `upload <target> <files...>`, `check <target>`, `uncheck <target>`, `snapshot [-i] [target]`, `find [role\|text\|css] <val> [--nth N]`, `get text\|title\|url\|html\|value\|attr`, `eval <func\|expr> [target]`, `wait <cond>`, `dialog [accept\|dismiss [prompt]]`, `dialog-accept [prompt]`, `dialog-dismiss`, `viewport [w h]`, `resize <w> <h>`, `set viewport\|device\|media\|offline`, `delete-data` |
| **Navigation** | `go-back`, `go-forward`, `reload` |
| **Keyboard** | `press <key>`, `keydown <key>`, `keyup <key>` |
| **Mouse** | `mousemove <x> <y>`, `mousedown [button]`, `mouseup [button]`, `mousewheel <dx> <dy>` |
| **Save as** | `screenshot [file] [target]`, `pdf [file]` |
| **Tabs** | `tab list\|new [url]\|select <id\|index>\|close [id\|index]`, `tab-list`, `tab-new [url]`, `tab-select <index\|id>`, `tab-close [index\|id]` |
| **Storage** | `state-save [file]`, `state-load <file>`, `cookies [list\|set\|delete\|clear]`, `cookie-list`, `cookie-get <name>`, `cookie-set <name> <val>`, `cookie-delete <name>`, `cookie-clear`, `storage local\|session [list\|get\|set\|delete\|clear]`, `localstorage-list\|get\|set\|delete\|clear`, `sessionstorage-list\|get\|set\|delete\|clear` |
| **Network** | `network`, `requests`, `request <index>`, `request-headers <index>`, `request-body <index>`, `response-headers <index>`, `response-body <index>`, `route <pattern> [options]`, `route-list`, `unroute [pattern]`, `network-state-set online\|offline` |
| **DevTools** | `console [min-level]`, `errors`, `downloads`, `dialogs`, `run-code [code]`, `exec <file\|->`, `run <file\|->`, `recording-start`, `recording-stop`, `tracing-start [categories]`, `tracing-stop [file]`, `video-start [file]`, `video-stop`, `video-chapter <title>`, `video-show-actions`, `video-hide-actions`, `render`, `show`, `terminal [--fps N]`, `pause-at <loc\|next\|--clear>`, `resume`, `step-over`, `generate-locator <target>`, `highlight <target> [--hide]`, `cdp <method> [json]` |
| **Install** | `install [skills] [options]`, `skills list\|show\|install`, `browser install`, `install-browser [browser]` |
| **Sessions** | `-s=<name> <cmd>`, `--profile <name>`, `list`, `profile list`, `close-all`, `kill-all` |
| **Global options** | `--help [command]`, `--json`, `--raw`, `--version`, `--tab <id>`, `--timeout <d>`, `--cdp <url>`, `--headed`, `--no-sandbox`, `--no-render`, `--listen <addr>`, `--port <n>`, `--auth-token <tok\|auto>`, `--embed-origin <origin>`, `--download-dir <dir>`, `--proxy <url>`, `--proxy-bypass <pat>`, `--close-external`, `--browser-level` |

## Output, live viewer, and cleanup

Commands emit JSON on stdout (except default text `snapshot`, `terminal`, and `--raw` which unwraps JSON strings). Failures emit `{"error":"..."}` on stderr with exit code 1.

Use `wok -s=research render` (or `wok -s=research show`) to get the live `/render` viewer URL. Stop temporary profiles with `wok -s=research stop` (preserves storage) or `wok -s=research delete-data` (wipes storage).

For full syntax, flags, and examples of every command, read [references/commands.md](references/commands.md).
