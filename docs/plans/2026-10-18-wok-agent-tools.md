---
title: Wok agent tooling and persistent emulation
status: active
---

# Authorized additions

User requested offline agent skills and install commands, automatic browser
provisioning, viewport/mobile support, and a DSL for both agent batches and E2E
with optional assertions. Keep product code and documentation branded as Wok.

## Implementation units

1. Embed the canonical skill and references; install into project/global agent
   directories without overwriting modified content unless force is explicit.
2. Discover/cache the official Stable headless shell; auto-install only if no
   browser exists. Lock globally, validate downloads/archive paths, publish
   atomically, and demonstrate offline reuse.
3. Persist profile-wide viewport/mobile metrics, touch and mobile UA through
   retained CDP sessions; restore on restart, apply to new tabs, support reset.
4. Execute strict YAML/JSON flows through existing CLI actions in-process.
   Support variables, result captures, tabs, optional polling assertions,
   fail-fast JSON reports and failure screenshots. Leave profiles open unless
   explicit cleanup is configured.
5. Update agent docs, bundled skill, examples, and benchmark instructions; run
   real multi-profile/mobile/tab/flow tests, viewer browser smoke, race tests,
   vet, portability build, and focused CE review of added runtime surfaces.

## Scope

No new browser engine, MCP server, arbitrary shell execution, visual assertion
engine, or full programming language. Mobile mode uses Chromium; dimensions,
DPR, touch, and mobile UA do not turn it into another browser engine.
