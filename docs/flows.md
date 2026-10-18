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

Files are limited to 1 MiB and 1,000 steps. Unknown fields, extra YAML documents,
and steps with more than one action fail validation. This is a sequence format:
there are no shell commands, loops, branches, or automatic retries of mutating
actions. Run only flows you intend to execute; `eval` can execute page JavaScript.

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

Use stable CSS selectors for stored flows. Snapshot refs change and belong to
a specific document/target; do not hardcode refs from an earlier run. Actions
execute in the current process, with CDP connections and profile locking per
command. A flow is not a transaction: coordinate other clients using that same
profile while it runs.

## Variables and captured results

`${NAME}` reads `--var NAME=VALUE` first, then an environment variable. Missing
variables fail rather than becoming empty strings. Variables do not execute shell
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

Captured object fields are available through dotted names. Captured values are
text substitution; for JavaScript values requiring escaping, prefer an explicit
assertion such as `expect.text` rather than interpolating values into code.

## Assertions

`expect` polls every 75 ms until its conditions pass or its timeout expires.
Mutating actions are not retried. A failed assertion stops the flow.

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
Assertions and element selectors currently address the main document.

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
