# Wok Command Reference

This reference documents every command, alias, and option available in the `wok` CLI.

## 1. Browser Sessions & Lifecycle

| Command | Description |
|---|---|
| `wok [-s=<name>] start [options]` | Start or reuse the profile browser and resident `/render` controller. |
| `wok [-s=<name>] open [url]` | Start the profile browser when called with no arguments, or navigate the active tab to `url`. |
| `wok [-s=<name>] status` | Return `profile`, `endpoint`, `pid`, `browser`, `targetId`, `renderUrl`, and `authRequired`. |
| `wok [-s=<name>] stop` / `wok close` | Stop the profile browser while retaining cookies and web storage on disk. |
| `wok [-s=<name>] delete-data` | Stop the profile browser and delete its user data directory, state, routes, and dialog policy. |
| `wok list` / `wok profile list` | List all known profiles and whether each browser is currently running. |
| `wok close-all` | Gracefully stop every running managed profile browser. |
| `wok kill-all` | Forcefully kill every managed profile browser process and clear stale state. |
| `wok [-s=<name>] attach [name\|cdp-url\|port]` | Persistently attach this profile to another running profile, local port, or external CDP HTTP/WebSocket URL; with no argument, show current attach state. |
| `wok [-s=<name>] detach` | Detach this profile from an attached external browser. |

```sh
wok -s=work open
wok list
wok -s=guest attach work
wok -s=guest detach
wok -s=work close
wok -s=work delete-data
wok close-all
```

## 2. Core Page & Element Actions

`TARGET` accepts a persistent snapshot reference (`@eN`) or a CSS selector matching one element in the main document. Snapshots include iframe contents (same- and cross-origin) after each `Iframe` node, and their refs act inside the frame. To reach into an iframe with CSS, chain selectors with ` >> `: `wok click '#payment >> button'` (nested: `'#outer >> #inner >> button'`). Chains work in every element command, `wait`, and flows.

| Command | Description |
|---|---|
| `wok goto <url>` / `wok open <url>` | Navigate the active tab to `<url>` (missing scheme defaults to `https://`). |
| `wok snapshot [-i] [target]` | Inspect accessibility nodes (`-i` for interactive roles only; `target` scopes to an element's subtree; `--json` for structured output). |
| `wok find <text>` | Find an element by visible text and assign/return its `@eN` ref. |
| `wok find role\|text\|css <value> [--nth N]` | Find an element by accessible role, visible text, or CSS selector (`--nth` is 1-based). |
| `wok click <target> [left\|right\|middle]` | Scroll into view, verify hit-test/enabled state, and click the element's center. |
| `wok dblclick <target> [left\|right\|middle]` | Double-click an unobscured, enabled element. |
| `wok hover <target>` | Move the pointer over the element's center. |
| `wok fill <target> <text>` | Focus an editable input/textarea/contentEditable and replace its contents. |
| `wok type <text>` | Insert text into the currently focused element. |
| `wok select <target> <value...>` | Select one or more `<select>` options by `value` or visible label. |
| `wok check <target>` | Check a checkbox, radio button, or ARIA switch if not already checked. |
| `wok uncheck <target>` | Uncheck a checkbox or ARIA switch if currently checked. |
| `wok drag <start> <end>` | Drag from `<start>` to `<end>` using pointer movement and HTML5 drag-and-drop transfer. |
| `wok drop <target> <file...>` | Drop one or more local files onto `<target>`. |
| `wok drop <target> --text <text>` | Drop `text/plain` data onto `<target>`. |
| `wok upload <target> <file...>` | Set local files on an `<input type="file">` (hidden inputs work), firing `input`/`change`. Any other target is clicked and the file chooser it opens is answered, which covers styled upload buttons and inputs created on click. Several files need a `multiple` input. `window.showOpenFilePicker()` is not supported. |
| `wok get text\|title\|url [selector]` | Read visible page/element text, document title, or URL. |
| `wok get attr <selector> <name>` | Read an attribute from an element. |
| `wok eval <js> [target]` | Evaluate JavaScript on the page, or pass `<target>` as the first argument to `<js>` (`el => ...`). Promises are awaited. |
| `wok wait <selector> [--state attached\|detached\|visible\|hidden]` | Wait for a selector to reach the requested state (default: `attached`). |
| `wok wait --text <t>\|--url <u>\|--fn <js>\|--load\|--network-idle [--network-idle-ms N]\|--download` | Wait for text, URL substring, JS predicate, load event, network quiet period, or download completion. |
| `wok dialog [accept\|dismiss [prompt]]` | Inspect or set the automatic response policy for future `alert`/`confirm`/`prompt` dialogs. |
| `wok dialog-accept [prompt]` | Accept future JavaScript dialogs (optionally supplying prompt text). |
| `wok dialog-dismiss` | Dismiss future JavaScript dialogs. |
| `wok resize <w> <h>` / `wok viewport [w h] [--mobile] [--dpr N]\|reset` | Set or reset persistent viewport and mobile emulation across tabs. |
| `wok set viewport\|device\|media\|offline [args]` | Apply device presets (`iphone-14`, `pixel-7`, `ipad-mini`, `desktop`), `media dark\|light\|no-preference`, or `offline on\|off`. |

## 3. Navigation, Keyboard & Mouse

| Command | Description |
|---|---|
| `wok go-back` | Navigate back one entry in the tab's session history. |
| `wok go-forward` | Navigate forward one entry in the tab's session history. |
| `wok reload` | Reload the active tab and wait for load completion. |
| `wok press <key>` | Press and release a key or shortcut (`Enter`, `Tab`, `Escape`, `Control+a`, `Meta+a`, `Shift+Tab`, `ArrowDown`, `F1`–`F12`). |
| `wok keydown <key>` | Dispatch a `keyDown` event (including standalone modifiers `Shift`, `Control`, `Alt`, `Meta`). |
| `wok keyup <key>` | Dispatch a `keyUp` event. |
| `wok mousemove <x> <y>` | Move the pointer to CSS pixel coordinates `(x, y)` and persist pointer position in profile state. |
| `wok mousedown [left\|right\|middle]` | Press and hold a mouse button at the current pointer coordinates. |
| `wok mouseup [left\|right\|middle]` | Release a mouse button at the current pointer coordinates. |
| `wok mousewheel <dx> <dy>` | Scroll the mouse wheel by `(dx, dy)` at the current pointer coordinates. |

## 4. Save As (Screenshots & PDF)

| Command | Description |
|---|---|
| `wok screenshot [file] [target]` | Save a PNG screenshot of the viewport or a specific `<target>` element. Defaults to `screenshot-YYYYMMDD-HHMMSS.png` when `file` is omitted. |
| `wok pdf [file]` | Export the active page as PDF (`printBackground: true`). Defaults to `page-YYYYMMDD-HHMMSS.pdf`. |

## 5. Tabs

`tab-select` and `tab-close` accept either a zero-based tab index from `tab-list` (`0`, `1`, ...) or a Chromium `targetId`.

| Command | Description |
|---|---|
| `wok tab-list` / `wok tab list` | List open tabs with `targetId`, `type`, `title`, and `url`. |
| `wok tab-new [url]` / `wok tab new [url]` | Open a new tab, select it, and optionally navigate to `url`. |
| `wok tab-select <index\|id>` / `wok tab select <index\|id>` | Persistently select the active tab for the profile. |
| `wok tab-close [index\|id]` / `wok tab close [index\|id]` | Close the specified tab (defaults to the active tab). |

## 6. Storage & Cookies

| Command | Description |
|---|---|
| `wok state-save [file]` | Save all browser cookies and per-origin `localStorage` to JSON (default: `storage-state.json`). |
| `wok state-load <file>` | Restore cookies and per-origin `localStorage` from a saved state JSON file. |
| `wok cookie-list` / `wok cookies [list]` | List browser cookies. |
| `wok cookie-get <name>` | Return a single cookie object by name. |
| `wok cookie-set <name> <val> [options]` / `wok cookies set ...` | Set a cookie (`--domain`, `--path`, `--url`, `--same-site Strict\|Lax\|None`, `--expires UNIX`, `--secure`, `--http-only`). |
| `wok cookie-delete <name> [--domain H] [--path P]` | Delete matching cookie(s). |
| `wok cookie-clear` / `wok cookies clear` | Clear all browser cookies. |
| `wok localstorage-list` | List all `localStorage` key/value pairs on the active origin. |
| `wok localstorage-get <key>` | Read a `localStorage` entry. |
| `wok localstorage-set <key> <val>` | Set a `localStorage` entry. |
| `wok localstorage-delete <key>` | Delete a `localStorage` entry. |
| `wok localstorage-clear` | Clear `localStorage` on the active origin. |
| `wok sessionstorage-list` | List all `sessionStorage` key/value pairs on the active origin. |
| `wok sessionstorage-get <key>` | Read a `sessionStorage` entry. |
| `wok sessionstorage-set <key> <val>` | Set a `sessionStorage` entry. |
| `wok sessionstorage-delete <key>` | Delete a `sessionStorage` entry. |
| `wok sessionstorage-clear` | Clear `sessionStorage` on the active origin. |

## 7. Network Inspection & Mocking

| Command | Description |
|---|---|
| `wok requests` / `wok network [--method M] [--url GLOB] [--type T] [--body]` | List recorded network requests for the active tab with 1-based `index`. Filters keep each `index`; `--body` adds `requestBody` and parsed `requestJson`. |
| `wok request <index>` | Inspect full request/response metadata, headers, and bodies for request `<index>`. |
| `wok request-headers <index>` | Return request headers for request `<index>`. |
| `wok request-body <index>` | Return request POST body for request `<index>`. |
| `wok response-headers <index>` | Return response headers for request `<index>`. |
| `wok response-body <index>` | Return text response body (or save binary response body to `response-<index>.<ext>`). |
| `wok route <pattern> [options]` | Mock or abort matching URLs (`--status N`, `--body TEXT`, `--body-file FILE`, `--content-type TYPE`, `--header K:V`, `--times N`, `--abort`). Globs match the full URL, so use `**/api/**`, not `*/api/*`. The newest matching route wins; a `--times` route stacks on same-pattern routes and expires after N hits. `route-list` shows `hits` per route. |
| `wok route-list` | List active network routes for the profile. |
| `wok unroute [pattern]` | Remove a matching route pattern, or all routes when `pattern` is omitted. |
| `wok network-state-set online\|offline` | Toggle offline network emulation. |

## 8. DevTools, Recording, Tracing, Video & Debugger

| Command | Description |
|---|---|
| `wok console [debug\|info\|warning\|error]` | Read recorded `console.*` messages, optionally filtered by minimum severity. |
| `wok errors` | Read recorded uncaught page exceptions and runtime errors. |
| `wok downloads` | Read recorded browser downloads (`guid`, `state`, `path`, `url`). |
| `wok dialogs` | Read recorded JavaScript dialogs (`type`, `message`, `accepted`, `promptText`). |
| `wok run-code [code]` | Execute newline-separated Wok command lines from the argument or stdin. |
| `wok exec <file\|->` | Execute newline-separated Wok command lines from a file or stdin. |
| `wok run <file\|-> [--var K=V] [--artifacts DIR] [--no-redact]` | Run a YAML/JSON action flow with optional E2E assertions, `if`/`repeat`/`each` control flow, and network steps. See [flows.md](flows.md). |
| `wok recording-start` | Record user clicks, fills, selects, checks, and keys in the page. |
| `wok recording-stop` | Stop recording and return both structured `actions` and a replayable `wok exec` `script`. |
| `wok tracing-start [categories]` | Start Chromium trace capture. |
| `wok tracing-stop [file]` | Stop trace capture and write the trace JSON file (default: `trace-YYYYMMDD-HHMMSS.json`). |
| `wok video-start [file]` | Record tab screencast video (`.avi` built-in Motion-JPEG; `.mp4`/`.webm` via `ffmpeg`). |
| `wok video-stop` | Stop video recording and write the video file (plus `.chapters.json` if chapters were added). |
| `wok video-chapter <title>` | Add a timestamped chapter marker and on-screen title card to the active video. |
| `wok video-show-actions` / `wok video-hide-actions` | Enable or disable visual action callouts and element outlines on the page during CLI actions. |
| `wok show` / `wok render` | Start or reuse the live HTTP viewer and return its `/render` URL. |
| `wok terminal [--fps N] [--frames N] [--width N]` | Interactive ANSI terminal viewer with mouse and keyboard forwarding. |
| `wok pause-at <script:line[:col]\|next\|--clear>` | Enable the JS debugger and set a URL/line breakpoint, pause on next statement, or clear breakpoints. |
| `wok resume` | Resume paused JavaScript execution. |
| `wok step-over` | Step over the current paused JavaScript statement and return the updated pause location. |
| `wok generate-locator <target>` | Compute a unique CSS selector, accessible role/name, and sample `wok` command for `<target>`. |
| `wok highlight <target> [--hide]` / `wok highlight --hide` | Draw or remove a visual highlight overlay on `<target>`. |
| `wok cdp <method> [json] [--browser-level]` | Send a raw CDP command to the active page session (or browser connection with `--browser-level`). |

## 9. Install & Global Options

| Command / Option | Description |
|---|---|
| `wok install [skills] [options]` / `wok skills install` | Install the bundled Wok skill (`--agent codex\|claude\|cursor\|all`, `--global`, `--path DIR`, `--force`). |
| `wok install-browser [chromium]` / `wok browser install` | Download or reuse the cached Chromium headless shell binary. |
| `-s=<name>`, `--session <name>`, `--profile <name>` | Select the isolated browser profile (default: `default`). |
| `--tab <id>` | Target a specific tab ID for this command without changing the profile's selected tab. |
| `--frame <iframe>` | Scope element commands, `eval`, `snapshot`, `find`, and `wait` to the document inside an iframe (CSS selector, `@ref`, or ` >> ` chain for nested frames). Navigation and tab commands still act on the tab. |
| `--cdp <url>` | Operate against an external HTTP or WebSocket CDP endpoint. |
| `--browser <path>` | Browser executable for `start` (overrides the auto-installed Chromium; also `WOK_BROWSER`). |
| `--headed` | Show the browser window (`start` only). |
| `--no-sandbox` | Disable the Chromium sandbox (`start` only); needed where unprivileged user namespaces are blocked, such as Ubuntu with AppArmor, unless the system browser is used. |
| `--no-render` | Start without the `/render` controller; also disables recording of events from before a command. |
| `--listen <host:port>` / `--port <n>` | Controller HTTP/CDP/MCP address (default loopback, automatic port). |
| `--auth-token <token\|auto>` | Require a bearer or query token on remote APIs. |
| `--embed-origin <origin>` | Allow embedding and API calls from an origin (repeatable). |
| `--download-dir <dir>` | Browser download directory. |
| `--proxy <url>` / `--proxy-bypass <patterns>` | HTTP(S)/SOCKS proxy (HTTP `user:pass` supported) and comma-separated bypass patterns. |
| `--close-external` | Allow `stop` against an explicitly supplied `--cdp` browser. |
| `--browser-level` | Send `cdp` commands to the browser instead of the active page. |
| `--timeout <duration>` | Bound total command execution time (e.g. `500ms`, `30s`; default `15s`, or `5m` for `start`/`install-browser`/`run`). |
| `--json` | Emit JSON for `snapshot` (all other non-terminal commands emit JSON by default). |
| `--raw` | Print string results without surrounding JSON quotes. |
| `--help [command]` | Show global help or command-specific usage (`wok --help click` or `wok click --help`). |
| `--version` | Emit the Wok version as JSON. |

Environment: `WOK_HOME` overrides the private state root, `WOK_BROWSER` the browser executable, and `WOK_BROWSER_CACHE` the shared browser download cache.
