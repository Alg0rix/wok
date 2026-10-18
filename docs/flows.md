# Wok flows: batch actions and E2E

`wok run` executes a YAML or JSON file using the same browser actions as the
interactive CLI. Assertions are optional: use a flow for a batch of agent
commands, an E2E test, or a sequence that mixes both.

```sh
wok run examples/login.wok.yaml \
  --var APP_URL=http://localhost:3000 \
  --var TEST_EMAIL=test@example.com

wok run examples/batch.wok.json --var PAGE_URL=https://example.com
cat examples/batch.wok.json | wok run - --var PAGE_URL=https://example.com
```

## File format

```yaml
version: 1
profile: e2e
close: false
viewport:
  width: 390
  height: 844
  mobile: true
  dpr: 3
steps:
  - name: Open login
    open: "${APP_URL}/login"
  - fill:
      selector: "#email"
      value: "${TEST_EMAIL}"
  - click: "#submit"
  - name: Check confirmation
    timeout: 5s
    expect:
      selector: "#success"
      text: "Berhasil"
```

`version` defaults to 1; other versions are rejected. `profile` defaults to
`default`. An explicit CLI `--profile` overrides the file. The runner starts or
reuses the selected browser before running steps; missing browsers are installed
automatically. It leaves the profile running by default. Set `close: true` only
when the flow owns that profile's lifecycle; this stops it after success or
failure without deleting cookies/storage.

`--cdp URL` attaches an external browser and skips startup. External flows cannot
configure persistent viewport emulation or use `close: true`. `--tab ID` targets
an exact page for actions. Default total timeout is five minutes; override with
`--timeout`. Step timeouts are 15 seconds for actions and 10 seconds for
assertions unless a step supplies `timeout`.

Files are limited to 1 MiB and 1,000 steps, counting steps nested in blocks.
Unknown fields, extra YAML documents, and steps with more than one action fail
validation. Flows can branch and loop (see [control flow](#control-flow)), but
there are no shell commands or automatic retries of mutating actions. Run only
flows you intend to execute; `eval` can execute page JavaScript.

## Actions

Each step contains exactly one action, with optional `name`, `timeout`, and
`save` metadata.

| Action | Example |
|---|---|
| Navigate | `open: https://example.com` |
| Click | `click: "#submit"` |
| Fill | `fill: {selector: "#email", value: "test@example.com"}` |
| Type in focused element | `type: hello` |
| Press key | `press: Enter` |
| Evaluate JavaScript | `eval: document.title` |
| Read data | `get: {kind: text, selector: "#result"}` |
| Read attribute | `get: {kind: attr, selector: "#next", attr: href}` |
| Wait for existence | `wait: "#ready"` |
| Save viewport image | `screenshot: success.png` |
| Snapshot | `snapshot: {interactive: true}` |
| Open tab | `tab: {new: "https://example.org"}` |
| Select tab | `tab: {select: "${details.targetId}"}` |
| Close tab | `tab: {close: "${details.targetId}"}` |
| Mock a request | `route: {pattern: "**/api/**", status: 200, body: "{}"}` |
| Remove routes | `unroute: "**/api/**"` or `unroute: true` (all) |
| List requests | `network: true` |
| Inspect a request | `request: 3` (index from `network`) |
| Branch | `if: {selector: "#banner"}` with `then:` / `else:` steps |
| Loop | `repeat: {times: 3, steps: [...]}`, `repeat: {until: {...}, steps: [...]}` |
| Iterate | `each: {items: [a, b], steps: [...]}` |

## Control flow

`if`, `repeat`, and `each` contain nested steps. Their conditions use the same
fields as [`expect`](#assertions) (`selector`, `text`, `contains`, `value`,
`count`, `visible`, `url`, `js`, `frame`) but are checked once, without polling. Add an
`expect` or `wait` step first when the page needs time to settle.

```yaml
steps:
  - if: {selector: ".cookie-banner", visible: true}
    then:
      - click: ".cookie-banner .accept"
    else:
      - eval: console.log('no banner')

  - repeat:
      times: 3                 # or while: {...} / until: {...}
      as: page                 # optional, default "loop"
      steps:
        - click: ".next"
        - expect: {js: "document.querySelectorAll('.row').length >= ${page.number} * 10"}

  - repeat:
      until: {selector: ".done"}
      max: 20                  # default 100; reaching it fails the step
      steps:
        - click: "#load-more"

  - each:
      items: [first follow-up, second]
      as: msg
      steps:
        - fill: {selector: ".chat-input", value: "${msg.item}"}
        - click: ".chat-send"
```

`while` checks before each iteration and `until` after it. Inside a loop,
`${NAME.index}` is the 0-based iteration, `${NAME.number}` is 1-based, and in
`each` `${NAME.item}` is the current item (object items expose fields as
`${NAME.item.FIELD}`). `items` is a YAML list or a string that resolves to a
JSON array, such as a saved result: `items: "${rows}"`. Loops run at most 10,000
iterations and blocks nest at most 8 levels. `save` is not allowed on a block
itself; steps inside a block can save values that later steps use.

Block steps appear in the report with `{"matched": BOOL}` or
`{"iterations": N}`. Nested steps carry their top-level `index` and a `path`
such as `3.then.1` or `4[2].1` (step 4, iteration 2, nested step 1). Error
messages use the same path.

## Network mocking

`route` mocks or aborts requests for the profile. Fields: `pattern` (required),
`status`, `body` or `bodyFile`, `contentType`, `headers` (a name→value map),
`times`, and `abort`. Patterns are globs matched against the full URL: `**`
matches anything and `*` matches anything except `/`. Use `**/api/**`;
`*/api/*` cannot match `http://host/api/x` and is rejected. A pattern without
`*` matches as a substring.

The newest matching route wins. A route with `times: N` handles N requests and
is then removed, letting older routes or the network answer. Timed routes stack,
so the last one added answers first; add per-request responses in reverse:

```yaml
steps:
  - unroute: true
  - route: {pattern: "**/api/**", status: 200, body: "{}"}
  - route:                                                          # answers second
      pattern: "**/api/chat/stream"
      times: 1
      contentType: text/event-stream
      body: "data: {\"delta\":\"hi\"}\n\n"
  - route: {pattern: "**/api/chat/stream", status: 500, times: 1}   # answers first
```

Here the first stream request gets 500, the second gets the event stream, and
later ones fall through to `**/api/**`. Adding an untimed route replaces the
untimed route with the same pattern in place, so timed routes above it keep
answering first. Routes persist with the profile, including partly used timed
routes, until they expire or `unroute` removes them; the CLI `route-list` shows
each route's `hits`. Route bodies are sent whole; when a test needs streaming
timing or responses computed from the request, run your own test server and let
the flow drive the browser against it. `network: true` lists requests with an
`index`; `request: INDEX` returns that request's headers and bodies, for example
to check a POST payload. Saved arrays such as `network` results are available
as `${net.0.url}`, `${net.length}`, or as `each` items.

Use stable CSS selectors for stored flows. Snapshot refs change and belong to
a specific document/target; do not hardcode refs from an earlier run. Actions
execute in the current process, with CDP connections and profile locking per
command. A flow is not a transaction: coordinate other clients using that same
profile while it runs.

## Variables and captured results

`${NAME}` reads `--var NAME=VALUE` first, then an environment variable. Missing
variables fail rather than becoming empty strings. Write `$${...}` for a literal
`${...}`, such as a JavaScript template literal inside `eval` or `js`:
`` eval: 'document.title = `n=$${n}`' ``. Variables do not execute shell
expansions. Prefer environment variables for secrets so values do not appear in
the process command line; reports do not list command arguments. Page results,
errors, and screenshots may still include page data, so keep artifacts private.

Use `save` to reuse a result later:

```yaml
steps:
  - tab:
      new: https://example.org
    save: details
  - tab:
      select: "${details.targetId}"
  - get:
      kind: title
    save: title
```

Captured object fields are available through dotted names; captured arrays
expose `${name.0}`, `${name.length}`, and the JSON text as `${name}`. Captured values are
text substitution; for JavaScript values requiring escaping, prefer an explicit
assertion such as `expect.text` rather than interpolating values into code.

## Assertions

`expect` polls every 75 ms until its conditions pass or its timeout expires.
Mutating actions are not retried. A failed assertion stops the flow; its error
lists the conditions that did not hold with the values last seen on the page,
for example `expect timed out: text: want "3 items", got "2 items"`. An `eval`
step that throws fails with the thrown error, such as
`browser JavaScript: Error: expected 2, got 0`.

```yaml
steps:
  - expect: {selector: "#ready"}
  - expect: {selector: "#success", text: "Berhasil"}
  - expect: {selector: ".message", contains: "Saved", visible: true}
  - expect: {selector: "input[name=email]", value: "test@example.com"}
  - expect: {selector: ".result", count: 3}
  - expect: {url: "https://example.com/done"}
  - expect: {js: "document.title === 'Done'"}
```

Text and value assertions compare exact strings; `contains` checks a substring.
A selector alone checks existence. `count: 0` checks absence. `visible` checks
layout/style visibility, not occlusion by another element. Text/value/count/
visibility predicates require a selector. Conditions in one `expect` combine
with AND. `js` must return a boolean; invalid JavaScript/selectors fail directly.
Selectors and assertions address the main document unless they reach into an
iframe. Chain the iframe and the element with ` >> `, as in element actions, or
set `frame` to run every condition, including `url` and `js`, inside the frame:

```yaml
steps:
  - click: "#checkout >> button[type=submit]"
  - expect: {selector: "#checkout >> .status", text: "Paid"}
  - expect: {frame: "#checkout", js: "window.paid === true"}
```

## Results and failure artifacts

The runner prints one JSON report to stdout with overall `ok`, the profile, and
per-step action, index, optional name, duration, result, and error. Exit code 0
means success; failures return a nonzero status with a summary on stderr. A
validation error has no browser actions. An execution failure may leave completed
page mutations; inspect the report before rerunning submissions.

On failure, the runner attempts a screenshot at
`.wok/artifacts/RUN_ID/failed-step-N.png`. Override the artifact directory with
`--artifacts DIR`. Screenshot failure is reported separately and never replaces
the original error. Cleanup and failure capture use bounded independent contexts
so a step timeout can still produce evidence. Explicit screenshots use the path
in the flow; create its parent directory before running.

For the lower-level CLI interaction model, see [the agent guide](agents.md).
