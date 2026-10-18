# Install the Wok agent skill

The binary embeds the `wok` skill and its supporting references. Installation
copies those local assets into an agent's skill directory. It does not require
Chrome, a running Wok profile, Node.js, or network access.

```sh
wok skills list
wok skills show
wok install skills                      # alias for skills install
wok skills install                       # this project: .agents/skills/wok
wok skills install --agent claude         # this project: .claude/skills/wok
wok skills install --agent cursor         # this project: .cursor/skills/wok
wok skills install --agent all            # all three project directories
wok skills install --global --agent codex # ~/.agents/skills/wok
wok skills install --path ./agent-skills  # ./agent-skills/wok
```

`--global` uses the user's home directory instead of the current project.
`--path` specifies a skills root and cannot be combined with `--global` or an
explicit `--agent`. `show wok` and `get wok` print the bundled `SKILL.md`.
`list` and `install` return JSON. Installation is scoped to this skill; it does
not modify the agent's general config or touch unrelated skills.

Installing the same content again is safe and reports unchanged files. If a
bundled file has been edited locally, installation fails before overwriting
anything. Use `--force` only when intentionally replacing your local edits:

```sh
wok skills install --agent claude --force
```

The installer rejects symlinks in destination paths and writes files atomically.
Review the returned paths, then reload skill discovery or start a new agent
session as required by your agent client. Keep the Wok executable on that
agent's PATH; the skill teaches usage but does not install the browser or binary.

For browser workflows, see [Wok for AI agents](agents.md).
