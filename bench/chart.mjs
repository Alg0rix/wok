// Renders bench/results.json as a static SVG bar chart for README/RESULTS.
// Usage: node bench/chart.mjs [results.json] [out.svg]
import { readFile, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// Fixed categorical order; light and dark steps validated as a set.
const MODES = [
  { name: 'wok warm subprocess', label: 'Wok CLI (warm)', light: '#2a78d6', dark: '#3987e5' },
  { name: 'playwright warm subprocess', label: 'Playwright script (warm)', light: '#eb6834', dark: '#d95926' },
  { name: 'playwright cold subprocess', label: 'Playwright script (cold)', light: '#1baf7a', dark: '#199e70' },
  { name: 'playwright resident', label: 'Playwright in-process', light: '#eda100', dark: '#c98500' },
];
const WORKLOADS = [
  { name: 'eval', label: 'Evaluate JavaScript' },
  { name: 'navigate', label: 'Navigate to page' },
  { name: 'workflow', label: 'Fill, click, wait, read' },
];

const escape = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);
const ms = (v) => (v < 10 ? v.toFixed(1) : Math.round(v).toString()) + ' ms';

export function renderChart(results) {
  const width = 760, left = 196, right = 72, plot = width - left - right;
  const bar = 20, gap = 8, panelHead = 30, panelGap = 22;
  const rows = new Map(results.rows.map((r) => [`${r.name}|${r.workload}`, r]));
  const max = Math.max(...results.rows.filter((r) => r.workload !== 'start').map((r) => r.medianMs));
  const step = max > 500 ? 250 : max > 200 ? 100 : 50;
  const domain = Math.ceil(max / step) * step;
  const x = (v) => (v / domain) * plot;

  const wok = rows.get('wok warm subprocess|eval'), pw = rows.get('playwright warm subprocess|eval');
  const headline = wok && pw ? `A Wok command is ${Math.round(pw.medianMs / wok.medianMs)}× faster than starting a Playwright script` : 'Wok vs Playwright';

  let y = 92;
  const panels = [];
  for (const w of WORKLOADS) {
    const top = y;
    let body = `<text class="panel" x="0" y="${y + 14}">${escape(w.label)}</text>`;
    y += panelHead;
    MODES.forEach((m, i) => {
      const r = rows.get(`${m.name}|${w.name}`);
      if (!r) return;
      const len = Math.max(2, x(r.medianMs));
      const cy = y + bar / 2;
      const rad = Math.min(4, len / 2);
      // Square at the baseline, 4px rounded data end.
      body += `<text class="mode" x="${left - 12}" y="${cy + 4}" text-anchor="end">${escape(m.label)}</text>`;
      body += `<path class="s${i}" d="M${left},${y}h${len - rad}a${rad},${rad} 0 0 1 ${rad},${rad}v${bar - 2 * rad}a${rad},${rad} 0 0 1 -${rad},${rad}h-${len - rad}z"><title>${escape(m.label)} · ${escape(w.label)}: median ${ms(r.medianMs)}, p95 ${ms(r.p95Ms)}</title></path>`;
      body += `<text class="value${i === 0 ? ' strong' : ''}" x="${left + len + 6}" y="${cy + 4}">${ms(r.medianMs)}</text>`;
      y += bar + gap;
    });
    panels.push({ top, bottom: y - gap, body });
    y += panelGap;
  }
  const plotBottom = y - panelGap + 4;
  let grid = '';
  for (let v = 0; v <= domain; v += step) {
    const gx = left + x(v);
    grid += `<line class="${v === 0 ? 'axis' : 'grid'}" x1="${gx}" x2="${gx}" y1="${panels[0].top + panelHead - 4}" y2="${plotBottom}"/>`;
    grid += `<text class="tick" x="${gx}" y="${plotBottom + 16}" text-anchor="middle">${v === 0 ? '0' : v + ' ms'}</text>`;
  }
  const legendY = 66;
  let legend = '', lx = 0;
  MODES.forEach((m, i) => {
    legend += `<rect class="s${i}" x="${lx}" y="${legendY - 9}" width="10" height="10" rx="2"/><text class="legend" x="${lx + 15}" y="${legendY}">${escape(m.label)}</text>`;
    lx += 26 + m.label.length * 6.6;
  });
  const height = plotBottom + 62;
  const note = `Median of ${results.samples} runs. Warm = browser already running, each sample is a new CLI/Node process; in-process = Playwright inside one long-lived script.`;
  const env = `${results.browser} · ${results.cpu} · ${results.platform}/${results.arch} · source: bench/results.json`;

  const css = (mode) => MODES.map((m, i) => `.s${i}{fill:${m[mode]}}`).join('');
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${width + 48}" height="${height}" viewBox="-24 0 ${width + 48} ${height}" role="img" aria-labelledby="t d" font-family="system-ui,-apple-system,'Segoe UI',sans-serif">
<title id="t">${escape(headline)}</title>
<desc id="d">Median latency in milliseconds per workload for Wok and Playwright modes. ${escape(note)}</desc>
<style>
.bg{fill:#fcfcfb}.title{fill:#0b0b0b;font-size:18px;font-weight:600}.sub,.mode,.legend{fill:#52514e;font-size:12.5px}
.panel{fill:#0b0b0b;font-size:13.5px;font-weight:600}.value{fill:#52514e;font-size:12px;font-variant-numeric:tabular-nums}.strong{fill:#0b0b0b;font-weight:600}
.tick,.note{fill:#898781;font-size:11px}.grid{stroke:#e1e0d9;stroke-width:1}.axis{stroke:#c3c2b7;stroke-width:1}${css('light')}
@media (prefers-color-scheme:dark){.bg{fill:#1a1a19}.title,.panel,.strong{fill:#f0efec}.sub,.mode,.legend,.value{fill:#c3c2b7}.grid{stroke:#2c2c2a}.axis{stroke:#383835}${css('dark')}}
</style>
<rect class="bg" x="-24" y="0" width="${width + 48}" height="${height}" rx="8"/>
<text class="title" x="0" y="30">${escape(headline)}</text>
<text class="sub" x="0" y="48">Median latency per action in milliseconds · lower is better</text>
${legend}
${grid}
${panels.map((p) => p.body).join('\n')}
<text class="note" x="0" y="${height - 30}">${escape(note)}</text>
<text class="note" x="0" y="${height - 14}">${escape(env)}</text>
</svg>
`;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const here = dirname(fileURLToPath(import.meta.url));
  const input = process.argv[2] || join(here, 'results.json');
  const output = process.argv[3] || join(here, 'benchmark.svg');
  await writeFile(output, renderChart(JSON.parse(await readFile(input, 'utf8'))));
  console.log(output);
}
