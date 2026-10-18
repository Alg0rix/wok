# Wok for AI agents

Use Wok when a shell-driven task needs a real browser: navigation, forms,
JavaScript, rendered screenshots, or a human watching the same page. Commands
attach directly through CDP; the running browser owns the session.

Install the bundled skill with `wok skills install`, or read it with
`wok skills show`. The skill is shipped inside the binary and does not fetch
instructions from the network. See [skill installation](skills.md).

## Start a task

Pick a profile and keep it on every command. Existing profiles may hold login
state; use one only when the task authorizes it. Different profile names mean
separate browsers, cookies, storage, tabs, and refs.

```sh
wok --profile research start
wok --profile research open https://example.com
wok --profile research snapshot -i
```

A successful `start` returns a JSON object containing `profile`, `pid`,
`endpoint`, `targetId`, `browser`, and `renderUrl`. It starts the browser only
when that profile is not already healthy. Use `--browser /absolute/path` or
`WOK_BROWSER` to select Chromium headless shell. If no browser is found, `start` downloads the
current Stable headless shell into a shared user cache and uses it automatically.
Use `wok browser install` to preinstall it. `--headed` opens a window with a
full Chrome/Chromium executable (headless shell itself has no desktop window).
The sandbox is enabled by default; only use `--no-sandbox` when the execution
environment needs it and that choice is authorized.

`start` waits for CDP and, by default, its HTTP viewer. `--no-render` omits the
viewer; `wok --profile research render` starts it later. Regular action commands
require a live browser and never cold-start one implicitly.

## Observe, act, verify

A typical form interaction:

```sh
wok --profile research snapshot -i
# Example output; use the actual refs from your own snapshot:
# @e8   textbox  "Email"
# @e9   button   "Continue"
wok --profile research fill @e8 'person@example.com'
wok --profile research click @e9
wok --profile research wait --selector '.confirmation'
wok --profile research snapshot -i
wok --profile research get text '.confirmation'
```

Snapshots describe accessibility roles/names, not pixels. `-i` keeps interactive
nodes; `--json` produces structured nodes. Ref IDs belong to the selected target
and document. Ref numbers increase on re-snapshotting; prior refs are discarded.
Take a fresh snapshot after navigation or a DOM update that replaces elements.
A ref from one profile or tab does not identify an element in another.

`click` rejects detached, disabled, or covered elements. Dismiss an overlay and
observe again when a hit-test fails. A CSS selector must match exactly one
element; use a narrower selector when it is ambiguous. A click does not wait for
an arbitrary navigation or application update. Wait for a concrete postcondition
such as a selector, then inspect the result. `wait --selector` tests existence,
not visibility or network idleness.

`fill` replaces editable contents and emits normal input events. `type` inserts
into the currently focused field. Use `press Enter`, `press Tab`, or a modifier
combination such as `Control+a` or `Meta+a`. Human activity in the live viewer
shares focus with the agent; coordinate actions when both use the same tab.

## Responsive layouts and mobile

```sh
wok --profile research viewport 1440 900
wok --profile research viewport 390 844 --mobile --dpr 3
wok --profile research viewport
wok --profile research viewport reset
```

These settings apply to all tabs in the profile and persist across CLI commands.
The viewer controller retains the CDP sessions that own the emulation, restores
saved settings on restart, and applies them to new tabs. `viewport` starts that
controller when needed. Mobile mode emulates metrics, touch, and a Chromium
mobile UA; it does not emulate a different browser engine. Without a viewport
meta tag, mobile pages may have a wider layout viewport than the device width.
Read `screen.width` as well as `innerWidth` when diagnosing that distinction.

Use [YAML/JSON flows](flows.md) for many actions in one invocation and optional
E2E assertions. `wok run FILE` emits a report; failure screenshots use an artifact
directory. Store stable selectors in flows, rather than refs from previous runs.

## Read results

```sh
wok --profile research get text
wok --profile research get text '#result'
wok --profile research get attr @e14 href
wok --profile research get title
wok --profile research get url
wok --profile research eval '({title:document.title, ready:document.readyState})'
wok --profile research screenshot /tmp/research.png
```

All successful outputs except default text snapshots are JSON. String results
are JSON strings, including their quotes. `eval` awaits promises; thrown
JavaScript exceptions are command failures. A viewport screenshot returns its
written path and byte count. Filesystem paths are relative to the invoking
shell unless absolute.

Failures are JSON on stderr with a nonzero exit code. Do not parse an error as a
successful value or assume a mutating action completed after a timeout. Verify
the observed state before retrying a submission or another irreversible action.

## Tabs and attachment

```sh
wok --profile research tab list
wok --profile research tab new https://example.org
wok --profile research tab select TARGET_ID
wok --profile research --tab TARGET_ID get title
wok --profile research tab close TARGET_ID
```

`tab new` selects its new tab. `tab select` persists the selection; `--tab` only
overrides this invocation. IDs are exact Chromium target IDs from `tab list`.
If a selected tab is closed elsewhere, a later command recovers to a remaining
page. An explicit `--tab` that is missing fails, so an agent cannot accidentally
operate on another page.

Attach to an already-running external browser with `--cdp`:

```sh
wok --cdp http://127.0.0.1:9222 snapshot -i
wok --cdp http://127.0.0.1:9222 click @e3
```

Keep the same endpoint on subsequent commands; external refs/selection have
their own local state. Attach mode never starts that browser. Commands disconnect
without closing it. External shutdown requires explicit
`wok --cdp URL stop --close-external`; use it only when the task owns that browser.

## Live viewer and HTTP controls

Run `wok --profile research render` to receive a complete `/render` URL. The
`renderUrl` field of `start`/`status` instead contains the base URL; append
`/render`. These are local loopback URLs. Use the environment's trusted forwarding
mechanism when the user is remote; it must preserve the viewer's local
Host/Origin expectations. Do not give a remote user an unusable localhost link.

A viewer opens the same real browser and supports tabs, navigation, keyboard,
mouse, wheel, and drag. `?embed=1&tab=TARGET_ID` hides its toolbar for same-origin
embedding. Optional `tab=` on HTTP endpoints controls exact targets.

```sh
curl "$RENDER_URL/render/viewport?tab=$TARGET_ID"
curl "$RENDER_URL/render/screenshot.png?tab=$TARGET_ID" -o /tmp/view.png
curl -X POST "$RENDER_URL/render/type?tab=$TARGET_ID" --data-binary 'hello'
curl -X POST "$RENDER_URL/render/key?tab=$TARGET_ID&key=enter"
```

Input coordinates use logical CSS pixels from `/render/viewport`, rather than
raw screenshot dimensions. Device pixel ratio can make those dimensions differ.
Scroll's query API uses `dy=-120` for roughly 60 pixels down; JSON input instead
uses CSS pixel deltas (`deltaY:60`). Endpoint details are in the
[HTTP table](../README.md#live-render).

## Timeouts, state, and cleanup

`--timeout` bounds the whole invocation, including waiting on the profile lock.
The default is 15 seconds for actions, five minutes for `start` and `browser
install` so one-time browser provisioning can complete:

```sh
wok --profile research --timeout 500ms wait --selector '#optional'
wok --profile research --timeout 30s open https://example.com
wok --profile research status
```

A busy profile serializes CLI commands; independent profiles can run concurrently.
If Chrome crashes, use `start` to recover it. When only the viewer exits, `start`
or `render` recreates the viewer while preserving the browser. A shutdown timeout
means shutdown is unconfirmed; saved identity stays available for inspection.

`WOK_HOME` selects the state root; otherwise use the OS user config directory
under `wok/profiles/PROFILE`. Cookies/storage are inside the `browser` directory;
metadata and logs are next to it. `WOK_BROWSER_CACHE` overrides the shared
executable cache independently of profile state. Do not edit private state or remove a profile
directory as a routine recovery step. `stop` preserves profile data on disk.
Stop profiles created for temporary tests when done; leave an existing shared
profile running unless the task requests its closure.

## Extended actions, network routing, and DevTools

Wok also supports the full interactive and DevTools command set (`-s=NAME` is a shorthand for `--profile NAME`, and `--raw` prints string results without JSON quotes):

- **Element & pointer**: `dblclick`, `hover`, `drag`, `drop`, `select`, `check`, `uncheck`, `upload`, `mousemove`, `mousedown`, `mouseup`, `mousewheel`, `keydown`, `keyup`, `generate-locator`, `highlight`
- **Navigation & output**: `goto`, `go-back`, `go-forward`, `reload`, `resize`, `screenshot [file] [target]`, `pdf [file]`
- **Storage & state**: `state-save`, `state-load`, `cookie-list|get|set|delete|clear`, `localstorage-list|get|set|delete|clear`, `sessionstorage-list|get|set|delete|clear`, `dialog-accept`, `dialog-dismiss`, `delete-data`
- **Network & DevTools**: `requests`, `request`, `request-headers`, `request-body`, `response-headers`, `response-body`, `route`, `route-list`, `unroute`, `network-state-set`, `console [min-level]`, `run-code`, `recording-start|stop`, `tracing-start|stop`, `video-start|stop|chapter`, `video-show-actions|video-hide-actions`, `pause-at`, `resume`, `step-over`
- **Sessions**: `open [url]`, `attach`, `detach`, `close`, `list`, `close-all`, `kill-all`, `show`, `install`, `install-browser`

Run `wok --help COMMAND` for any command's usage.

## Limits and protocol escape hatch

CSS/ref element actions target the main document. The snapshot is a flat accessibility listing (`snapshot [target]` scopes to a subtree). Use a full browser screenshot when a task depends on appearance.

For a supported CDP operation that has no convenience command:

```sh
wok --profile research cdp Runtime.evaluate '{"expression":"document.title","returnByValue":true}'
wok --profile research --browser-level cdp Browser.getVersion
```

Raw CDP sends to the active page by default. `--browser-level` addresses the
browser. Use protocol parameters appropriate to the installed engine; raw
commands do not acquire additional user authorization or validate page
postconditions for the agent.
