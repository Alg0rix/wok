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

For an external browser, consistently replace `--profile research` with `--cdp ENDPOINT`. Local `start` and `render` manage Wok-owned profiles and are unavailable with `--cdp`. Closing an external browser requires the explicit `--close-external` option and should only be done when the task owns that lifecycle.
