# Additional Wok commands

## Tabs and timeouts

Keep the same profile on all commands. `tab list` returns target IDs; select one persistently or use `--tab ID` for a single invocation. Take a fresh snapshot after changing the active tab.

```sh
wok --profile research tab list
wok --profile research tab new https://example.org
wok --profile research tab select TARGET_ID
wok --profile research --tab TARGET_ID get title
wok --profile research --timeout 30s wait --selector '#ready'
```

`tab close ID` closes that tab. Avoid closing tabs owned by other work. `open` waits for the page load event and defaults to HTTPS when a scheme is omitted; specify `http://` for HTTP development servers.

## Keyboard and text

`fill` replaces a field's content; `type` inserts into the focused editable element. Use `press` for keys and combinations:

```sh
wok --profile research press 'Control+a'
wok --profile research type 'replacement'
wok --profile research press 'Shift+Tab'
wok --profile research press Escape
```

Supported keys include Enter, Tab, Escape, Backspace, Delete, Insert, arrows, Home, End, PageUp, PageDown, Space, F1–F12, and individual characters. Modifier names include Control/Ctrl, Meta/Command/Cmd, Shift, and Alt/Option. Use Meta for platform shortcuts that require it. Quote key combinations and shell-sensitive text.

## Live `/render` viewer

```sh
wok --profile research render
wok --profile research status
```

`render` returns `url`, a complete viewer URL ending in `/render`. `status` and `start` return `renderUrl`, a base URL; append `/render` to open the viewer. A loopback URL is local to the browser host; when the user accesses a remote host, publish the viewer through that environment's approved port-sharing mechanism and share the resulting URL. Do not substitute localhost in a remote user's link. `start --no-render` omits the viewer; `render` can start it later. Viewer access controls the running browser.

## Raw CDP

The default raw command targets the active page. `--browser-level` sends a method to the browser connection:

```sh
wok --profile research cdp Page.getLayoutMetrics
wok --profile research cdp Runtime.evaluate '{"expression":"location.href","returnByValue":true}'
wok --profile research --browser-level cdp Browser.getVersion
```

Pass one JSON object argument for parameters; do not interpolate untrusted strings into shell commands or JavaScript. Raw CDP can mutate the browser and bypass the convenience commands' element checks; use the method required by the task.

For an external browser, consistently replace `--profile research` with `--cdp ENDPOINT` (or run `wok -s=research attach ENDPOINT` once and `wok -s=research detach` when finished). Local `start` and `render` manage Wok-owned profiles and are unavailable with `--cdp`. Closing an external browser requires the explicit `--close-external` option and should only be done when the task owns that lifecycle.

## Extended element, navigation, storage, network, and DevTools commands

```sh
# Sessions & lifecycle
wok -s=research open [url]            # start session (or navigate if url given)
wok list                              # list profiles and running status
wok -s=research attach [name|url]     # attach profile to another profile or CDP endpoint
wok -s=research detach
wok -s=research close                 # alias for stop
wok -s=research delete-data           # stop and wipe profile browser storage/state
wok close-all                         # gracefully stop all running profiles
wok kill-all                          # forcefully kill all profile browser processes

# Element interactions & navigation
wok -s=research goto https://example.com
wok -s=research click @e2 [left|right|middle]
wok -s=research dblclick @e2 [left|right|middle]
wok -s=research hover @e2
wok -s=research drag '#source' '#target'
wok -s=research drop '#zone' file.txt
wok -s=research drop '#zone' --text 'dropped text'
wok -s=research select '#country' 'ID'
wok -s=research check '#agree'
wok -s=research uncheck '#agree'
wok -s=research upload '#file' a.pdf b.png
wok -s=research snapshot [-i] [target]
wok -s=research find 'Submit'
wok -s=research eval 'el => el.textContent' @e2
wok -s=research go-back
wok -s=research go-forward
wok -s=research reload
wok -s=research resize 1280 720

# Raw keyboard & mouse
wok -s=research keydown Shift
wok -s=research keyup Shift
wok -s=research mousemove 200 150
wok -s=research mousedown [left|right|middle]
wok -s=research mouseup [left|right|middle]
wok -s=research mousewheel 0 120

# Save as & tabs
wok -s=research screenshot [file.png] [target]
wok -s=research pdf [page.pdf]
wok -s=research tab-list
wok -s=research tab-new [url]
wok -s=research tab-select 0
wok -s=research tab-close [0]

# Storage & dialogs
wok -s=research state-save [state.json]
wok -s=research state-load state.json
wok -s=research cookie-list | cookie-get NAME | cookie-set NAME VAL | cookie-delete NAME | cookie-clear
wok -s=research localstorage-list | localstorage-get K | localstorage-set K V | localstorage-delete K | localstorage-clear
wok -s=research sessionstorage-list | sessionstorage-get K | sessionstorage-set K V | sessionstorage-delete K | sessionstorage-clear
wok -s=research dialog-accept [prompt]
wok -s=research dialog-dismiss

# Network & DevTools
wok -s=research requests
wok -s=research request 1 | request-headers 1 | request-body 1 | response-headers 1 | response-body 1
wok -s=research route '**/api/*' --status 200 --body '{"ok":true}'
wok -s=research route-list
wok -s=research unroute ['**/api/*']
wok -s=research network-state-set offline|online
wok -s=research console [debug|info|warning|error]
wok -s=research run-code 'open https://example.com'
wok -s=research recording-start && wok -s=research recording-stop
wok -s=research tracing-start && wok -s=research tracing-stop [trace.json]
wok -s=research video-start [out.avi] && wok -s=research video-chapter 'Intro' && wok -s=research video-stop
wok -s=research video-show-actions | video-hide-actions
wok -s=research pause-at app.js:10 | resume | step-over | pause-at --clear
wok -s=research generate-locator @e2
wok -s=research highlight @e2 [--hide]
```

Use `--raw` to print string results without JSON quotes, and `wok --help COMMAND` for command-specific usage.

