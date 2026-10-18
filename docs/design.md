# Persistent CDP agent browser CLI

## Goal

Create a persistent browser CLI for agents. Start Chrome/Chromium once,
then execute independent CLI commands against the same running browser.
Pages, cookies, storage, and tabs survive CLI process exit. Use CDP directly;
do not depend on Playwright or Puppeteer.

Product name: `wok`. Use a small client controlling an installed Chromium
engine rather than bundling a desktop browser. Playwright can also attach to a persistent browser, so cold
starts are a lifecycle choice rather than an inherent Playwright limitation.

## Approach

Use Go for a single compiled CLI binary and one small WebSocket transport
dependency. Chrome/Chromium is an external prerequisite. Native Node.js with
WebSocket would minimize implementation effort but require Node at runtime.
Embedding Chromium would simplify browser provisioning but increase package
size and build complexity. The Go client best matches the lightweight goal.

Chrome owns browser state. A separate small Go HTTP viewer process per profile
serves the required persistent `/render` endpoints; it exits when Chrome closes. Each command
discovers the browser endpoint, opens a WebSocket, executes CDP requests,
closes its connection, and exits without terminating Chrome.

## Browser lifecycle

`wok start` starts detached Chrome, headless by default, with a dedicated
profile directory and a loopback debugging endpoint. Multiple named profiles
can run concurrently. Each profile has its own browser process, user data
directory, debugging port, selected tab, refs, state file, and lifecycle lock.
Cookies, localStorage, tabs, and authentication are isolated between profiles.
The default profile is `default`; the global `--profile NAME` option selects
another profile for every command. Names are validated to prevent directory
traversal. Repeated starts of the same profile return its existing healthy
browser; starting another profile does not restart or stop existing ones.
Other commands require a running browser and
provide an actionable start instruction when none exists. This keeps startup
explicit and makes unintended relaunches visible.

Use Chrome's ephemeral debugging port and DevToolsActivePort discovery for
managed browsers. Store session metadata atomically in a private per-user
state directory: endpoint, browser identity, profile, selected tab, and refs.
Serialize managed-session commands with an OS-released lock so concurrent
starts cannot create duplicate browsers and ref updates cannot race. Bound
lock acquisition and startup by timeouts. Never kill a process based solely
on a stored PID. Revalidate the browser endpoint and identity before reuse.

`stop` closes only the managed browser through CDP. Externally attached
browsers require an explicit close flag. Never delete the user's profile on
stop. A stale session reports an error; `start` can recover it. Headed mode,
browser executable override, named sessions, and an explicit no-sandbox
option are supported. Do not disable Chrome's sandbox by default. Stopping
one profile leaves other profiles running, and restarting it reuses its
on-disk cookies and storage. `profile list` reports known profiles and whether
their browser is running. `--session` is not a separate alias or concept in
the initial interface: a managed profile is the unit of browser reuse.

`--cdp` accepts an HTTP discovery endpoint or browser WebSocket URL for an
existing local or remote CDP browser. No proxy server or public listener is
introduced by this CLI.

## Initial command interface

All commands produce structured JSON on stdout; errors use stderr and a
nonzero exit code. `snapshot` also supports compact text for agent prompts.
Global options include profile, CDP endpoint, target tab, and timeout.

```sh
wok start
wok open https://example.com
wok snapshot -i
wok click @e1
wok fill @e2 'hello'
wok type 'additional text'
wok press Enter
wok get text
wok get title
wok get url
wok get attr @e1 href
wok eval 'document.title'
wok wait --selector '.results'
wok screenshot page.png
wok tab list
wok tab new https://example.org
wok tab select TARGET_ID
wok tab close TARGET_ID
wok cdp Runtime.evaluate '{"expression":"1+1","returnByValue":true}'
wok status
wok stop

# Independent browsers running at the same time
wok --profile work start
wok --profile personal start
wok --profile work open https://example.com
wok --profile personal open https://example.org
wok profile list
wok --profile work stop
# personal keeps running; work retains its profile data on disk
```

CSS selectors and snapshot refs are accepted for element actions. Navigation
normalizes addresses lacking a scheme, waits for a defined load condition,
and reports navigation errors. Explicit timeouts bound every CDP request,
navigation, and selector wait. Raw CDP defaults to the active page session
and supports an explicit browser-level option.

## Snapshots and actions

Use the accessibility tree to describe roles and accessible names. Interactive
snapshots filter to actionable nodes. Associate refs with backend DOM node
IDs, target identity, and the document identity in session metadata. Refs
persist across CLI processes but fail after navigation or node removal;
ref numbers must not silently resolve to a different element. Re-snapshotting
refreshes the mapping. Explicitly report inaccessible cross-origin frame
content in the first version rather than silently claim complete coverage.

Resolve element actions through DOM/CDP, scroll into view, verify enabled
state and clickable geometry, and hit-test before dispatching real input.
Covered, detached, ambiguous, or stale targets fail with useful messages.
Fill focuses an editable target, selects its contents, and uses CDP text
input so ordinary input events fire. Type inserts into the focused element.
Define supported key names and modifiers in help. Avoid JavaScript click
shortcuts that bypass browser input behavior.

## Components

- CLI: argument parsing, help, JSON output, and exit status.
- Session: private state files, locking, browser discovery, launch and stop.
- CDP transport: request IDs, response routing, events, context deadlines,
  connection close propagation, and flattened target sessions.
- Browser actions: navigation, tabs, evaluation, input, waits, screenshots.
- Snapshot: accessibility representation and persistent DOM refs.

Keep modules direct and small. Do not introduce a generic plugin framework,
MCP server, browser installer, or custom browser engine. The HTTP viewer is
required for live `/render` (HTML, PNG/PPM, MJPEG, pointer/keyboard control).

## Verification

Unit tests cover parsing, session serialization, stale identity detection,
and CDP request/error/timeout/close behavior with a local mock WebSocket.
Integration tests use an installed Chromium and a local fixture server.
Run CLI commands as separate OS processes and assert the browser PID and
endpoint remain constant while DOM changes, cookies, and localStorage
survive. Verify idempotent and concurrent start, tabs, navigation, refs across
processes, stale refs after navigation, covered click rejection, form input,
selector wait timeout, screenshot output, disconnect errors, and clean stop.
Start two profiles concurrently and assert distinct browser processes and
endpoints, isolated cookies/storage/refs/tabs, and that stopping one leaves
the other healthy. Restart a stopped profile and verify persisted storage.

Measure cold start separately from repeated warm commands. Report observed
timings and environment without promising an unmeasured speed advantage.
Document installation, supported platforms verified locally, browser
discovery, session paths, errors, command coverage, and limitations.

## Acceptance

A user can build one CLI binary, start an installed browser once, run the
documented agent interaction loop through separate shell invocations, and
stop the browser explicitly. Tests demonstrate reuse and state preservation.
The runtime has no Playwright/Puppeteer dependency and commands never close
the browser merely because their CDP connection ends.
