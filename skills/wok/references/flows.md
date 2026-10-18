# Wok Flow Reference

`wok run FILE|- [--var K=V] [--artifacts DIR] [--no-redact]` executes a YAML or
JSON flow: a batch of browser actions with optional assertions, branches,
loops, and network mocks. It prints one JSON report (`ok`, `profile`, `steps`
with `index`, `path`, `action`, `durationMs`, `result`, `error`) and exits
nonzero on failure, with a screenshot in `.wok/artifacts/RUN_ID/` or `--artifacts DIR`.

```yaml
version: 1            # optional; only 1 is accepted
profile: e2e          # default "default"; CLI --profile overrides
close: false          # true stops the profile after the run
redact: true          # false keeps variable/saved values in error messages
viewport: {width: 390, height: 844, mobile: true, dpr: 3}   # optional
steps:
  - name: Open login            # optional label
    open: "${APP_URL}/login"
  - fill: {selector: "#email", value: "${TEST_EMAIL}"}
  - click: "#submit"
  - expect: {selector: "#success", text: "Welcome"}
    timeout: 5s                 # default 15s for actions, 10s for expect/blocks
```

Each step has exactly one action plus optional `name`, `timeout`, and `save`.
`TARGET` is a CSS selector or `@eN` ref; use `"#frame >> .el"` for iframes.
Limits: 1 MiB, 1,000 steps (nested included), 10,000 loop iterations, 8 nesting levels.

## Actions

| Action | Value |
|---|---|
| `open` | URL |
| `click`, `dblclick`, `hover`, `check`, `uncheck` | TARGET |
| `fill` | `{selector, value}` |
| `type` | text for the focused element |
| `press`, `keydown`, `keyup` | key such as `Enter`, `Shift`, `Control+a` |
| `select` | `{selector, value}` or `{selector, values: [..]}` (value or label) |
| `drag` | `{from, to}` |
| `upload` | `{selector, files: [..]}`: a file input (hidden is fine) or any element whose click opens a file chooser |
| `drop` | `{selector, files: [..]}` or `{selector, text}` |
| `go-back`, `go-forward`, `reload` | `true` |
| `tab` | `{new: URL}`, `{select: ID}`, or `{close: ID}` |
| `set` | one of `{device: NAME}`, `{media: dark\|light\|no-preference}`, `{offline: BOOL}` |
| `dialog` | `{accept: BOOL, promptText?}` policy for future dialogs |
| `eval` | JavaScript expression; promises awaited; throw fails the step |
| `get` | `{kind: title\|url\|text\|html\|value\|attr, selector?, attr?}` |
| `find` | `{kind: role\|text\|css, value, nth?}` |
| `snapshot` | `true` or `{interactive: BOOL}` |
| `wait` | selector string, or exactly one of `{selector, state?}`, `{text}`, `{url}`, `{fn}`, `{load: true}`, `{networkIdle: true, idleMs?}`, `{download: true}` |
| `expect` | condition (below); polls every 75 ms until timeout |
| `console`, `errors`, `downloads`, `dialogs` | `true`: read recorded events |
| `cookies` | `{action: list\|set\|delete\|clear, name?, value?, domain?, path?, url?, secure?, httpOnly?, sameSite?, expires?}` |
| `storage` | `{area: local\|session, action: list\|get\|set\|delete\|clear, key?, value?}` |
| `state-save`, `state-load` | JSON file of cookies and localStorage |
| `screenshot`, `pdf` | output file |
| `route` | `{pattern, status?, body? \| bodyFile?, contentType?, headers?: {K: V}, times?, abort?}` |
| `unroute` | pattern, or `true` for all |
| `network` | `true` or `{method?, url? (glob), type?, body?: BOOL}` |
| `request` | request index from `network` |
| `if` | condition, with `then:` and optional `else:` step lists |
| `repeat` | `{times \| while \| until, max?, as?, steps}` |
| `each` | `{items: [..] \| "${saved}", as?, steps}` |

CLI-only (use `wok exec` or plain commands around `wok run`): raw mouse events,
tracing, video, recording, debugger, `highlight`, `generate-locator`, `cdp`.
With `--cdp`, flows cannot use `console`, `errors`, `network`, `downloads`,
`dialogs`, `dialog`, `set`, `route`, `unroute`, `request`, `wait` on network
idle/download, `close`, `viewport`, or network conditions.

## Conditions (`expect`, `if`, `repeat`)

Fields: `selector`, `text` (exact), `contains`, `value`, `count` (`0` = absent),
`visible`, `url`, `js` (boolean expression), and `frame` (run all inside an
iframe). All fields must hold (AND). `if`/`repeat` check once, without polling.
`network: {method?, url?, type?, count?}` stands alone and waits for matching
requests (at least one, or exactly `count`). Failures report what the page showed:
`expect timed out: text: want "3 items", got "2 items"`.

## Variables

- `${NAME}` reads `--var NAME=V`, then the environment; missing variables fail.
- `$${...}` is a literal `${...}` (JavaScript template literals).
- `save: NAME` stores a step result: `${NAME}`, `${NAME.field}`, `${NAME.0}`, `${NAME.length}`.
- `${NAME|json}` inserts a JSON literal for use inside `eval`/`js`.
- Loops expose `${as.index}` (0-based), `${as.number}` (1-based), `${as.item}`; the default `as` is `loop`.
- Errors redact `--var`/env values and saved text; `redact: false` or `--no-redact` shows them.

## Files

Paths a step reads (`upload`/`drop` files, `route.bodyFile`, `state-load`)
resolve from the flow file's directory; paths it writes (`screenshot`, `pdf`,
`state-save`) from the working directory. Flows from stdin use the working
directory. `upload` cannot answer `window.showOpenFilePicker()`; use the page's
file input or `drop`.

## Recipes

```yaml
steps:
  - unroute: true
  - route: {pattern: "**/api/**", status: 200, body: "{}"}       # fallback
  - route: {pattern: "**/api/send", status: 500, times: 1}       # first call fails
  - open: "${APP_URL}"
  - if: {selector: ".cookie-banner", visible: true}
    then: [{click: ".cookie-banner .accept"}]
  - upload: {selector: "button.attach", files: [fixtures/cv.pdf]}
  - each:
      items: [hello, again]
      as: msg
      steps:
        - fill: {selector: textarea, value: "${msg.item}"}
        - click: button.send
  - expect: {network: {method: POST, url: "**/api/send", count: 2}}
  - network: {method: POST, url: "**/api/send", body: true}
    save: sent
  - expect: {js: '${sent.1.requestJson|json}.message === "again"'}
```
