# Wok vs Playwright benchmark

Recorded 2026-10-18T04:06:51.870Z. Intel(R) Xeon(R) CPU E5-2695 v4 @ 2.10GHz; 32 logical CPUs; linux/x64.

Engine: Google Chrome for Testing 155.0.8059.84. Playwright 1.64.1; Node v24.20.0; 15 samples per row.

| Mode | Workload | Median (ms) | p95 (ms) |
|---|---|---:|---:|
| wok warm subprocess | eval | 13.16 | 14.56 |
| playwright warm subprocess | eval | 666.02 | 691.37 |
| playwright cold subprocess | eval | 820.07 | 871.87 |
| playwright resident | eval | 2.58 | 4.26 |
| wok warm subprocess | navigate | 65.94 | 82.59 |
| playwright warm subprocess | navigate | 742.07 | 896.15 |
| playwright cold subprocess | navigate | 845.29 | 1125.95 |
| playwright resident | navigate | 65.72 | 98.87 |
| wok warm subprocess | workflow | 133.07 | 149.37 |
| playwright warm subprocess | workflow | 853.65 | 1005.46 |
| playwright cold subprocess | workflow | 913.26 | 1013.89 |
| playwright resident | workflow | 166.91 | 184.92 |
| wok idempotent start | start | 11.66 | 12.77 |

Wok first start (browser + idle render service): 145.40 ms. Binary: 11.13 MiB (unstripped).

## Method

- Local fixture served with Cache-Control: no-store. Same headless-shell executable for every mode. No external network page loads.
- Subprocess timings include process startup, module loading (Playwright), CDP connection, operation, disconnection, process exit; measured by the parent.
- Wok warm uses one CLI invocation for eval/navigation; workflow is four CLI invocations. Playwright workflow executes inside one Node invocation.
- Playwright cold launches and closes a new browser per sample; eval also navigates to fixture inside cold timing.
- Playwright warm connects to the same persistent browser used by Wok; resident excludes process startup and CDP connection.
- Wok browser and render service launched once. Viewer stream is inactive during benchmarks. Browser PID and endpoint checked unchanged at end.
- Two unrecorded warmup samples per warm case and one per cold case. Samples are sequential, grouped by mode rather than randomized.
- Memory is one idle Linux /proc snapshot, not peak memory. RSS sums shared pages multiple times; PSS apportions shared pages.
- Sandbox disabled for all browser launches in this test environment; no claim about production websites or anti-bot behavior.

Memory snapshot and every raw latency sample: [results.json](results.json).

## Interpretation

Wok optimizes shell-driven agent commands and profile reuse. Playwright also supports persistent browser attachment; its resident mode is the useful control for separating lifecycle cost from protocol/action cost. Cold-launch measurements must not be presented as evidence that Playwright always starts cold. This small local fixture does not establish performance on real-world sites.
