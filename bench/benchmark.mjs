import { chromium } from 'playwright-core';
import { workload } from './playwright-command.mjs';
import { createServer } from 'node:http';
import { execFile as execFileCallback } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir, cpus, platform, arch, totalmem } from 'node:os';
import { resolve, dirname, join, relative, basename, sep } from 'node:path';
import { renderChart } from './chart.mjs';
import { fileURLToPath } from 'node:url';
import { performance } from 'node:perf_hooks';
import assert from 'node:assert/strict';

const execFile = promisify(execFileCallback);
const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..');
const binary = resolve(process.env.WOK_BIN || join(root, 'bin/wok'));
let browserPath = process.env.WOK_BENCH_BROWSER ? resolve(process.env.WOK_BENCH_BROWSER) : null;
const samples = Number(process.env.WOK_BENCH_N || 20);
if (!Number.isInteger(samples) || samples < 3) throw Error('WOK_BENCH_N must be an integer >=3');
const home = await mkdtemp(join(tmpdir(), 'wok-bench-'));
const env = { ...process.env, WOK_HOME: home, WOK_BENCH_BROWSER: browserPath || '' };
const fixture = createServer((req, res) => {
  res.setHeader('Content-Type', 'text/html'); res.setHeader('Cache-Control', 'no-store');
  res.end(`<!doctype html><title>Wok benchmark</title><label>Name<input id="name"></label><button id="go" onclick="document.querySelector('#result').textContent=document.querySelector('#name').value">Submit</button><output id="result"></output>`);
});
await new Promise(r => fixture.listen(0, '127.0.0.1', r));
const url = `http://127.0.0.1:${fixture.address().port}/`;
const rows = [];
let persistentPW;
const wok = async (...args) => {
  const { stdout } = await execFile(binary, ['--profile', 'bench', ...args], { env, timeout: 30000 });
  return JSON.parse(stdout);
};
const summary = values => {
  const sorted = [...values].sort((a,b) => a-b);
  return { medianMs: sorted.length % 2 ? sorted[Math.floor(sorted.length/2)] : (sorted[sorted.length/2-1] + sorted[sorted.length/2])/2, p95Ms: sorted[Math.ceil(sorted.length*0.95)-1], minMs: sorted[0], maxMs: sorted.at(-1) };
};
async function measure(name, kind, fn, warmups=2) {
  for (let i=0; i<warmups; i++) await fn();
  const timings=[];
  for (let i=0; i<samples; i++) { const t=performance.now(); await fn(); timings.push(performance.now()-t); }
  const row = { name, workload:kind, samples, ...summary(timings), timingsMs:timings };
  rows.push(row); console.log(`${name.padEnd(26)} ${kind.padEnd(10)} median ${row.medianMs.toFixed(2)}ms  p95 ${row.p95Ms.toFixed(2)}ms`);
}
async function processMemory(pid) {
  try {
    const data = await readFile(`/proc/${pid}/smaps_rollup`, 'utf8');
    const kb = name => Number(data.match(new RegExp(`^${name}:\\s+(\\d+)`, 'm'))?.[1] || 0);
    return { rssBytes:kb('Rss')*1024, pssBytes:kb('Pss')*1024 };
  } catch { return null; }
}
async function treeMemory(pid) {
  if (platform() !== 'linux') return null;
  const children = new Map();
  for (const dir of await readdir('/proc')) {
    if (!/^\d+$/.test(dir)) continue;
    try {
      const data=await readFile(`/proc/${dir}/stat`,'utf8');
      const ppid=Number(data.slice(data.lastIndexOf(')')+2).split(' ')[1]);
      if (!children.has(ppid)) children.set(ppid,[]);children.get(ppid).push(Number(dir));
    } catch {}
  }
  const pids=[];function visit(p){pids.push(p);for(const child of children.get(p)||[]) visit(child);}visit(pid);
  const totals={processes:pids.length,rssBytes:0,pssBytes:0};
  for(const p of pids){const m=await processMemory(p);if(m){totals.rssBytes+=m.rssBytes;totals.pssBytes+=m.pssBytes;}}
  return totals;
}
try {
  if (!browserPath) {
    const installed=await execFile(binary,['browser','install'],{env,timeout:300000});
    browserPath=JSON.parse(installed.stdout).path; env.WOK_BENCH_BROWSER=browserPath;
  }
  const startup=performance.now();
  const state=await wok('start','--browser',browserPath,'--no-sandbox');
  const firstStartMs=performance.now()-startup;
  await wok('open',url);
  const browserVersion=(await execFile(browserPath,['--version'])).stdout.trim();
  const pwVersion=JSON.parse(await readFile(join(here,'node_modules/playwright-core/package.json'),'utf8')).version;
  console.log(`Engine: ${browserVersion}; Playwright ${pwVersion}; samples=${samples}`);
  // Capture memory separately before importing/connecting a resident client.
  const ready=JSON.parse(await readFile(join(home,'profiles/bench/render-ready.json'),'utf8'));
  const memory={browserIdle:await treeMemory(state.pid),renderIdle:await processMemory(ready.pid)};
  for(const kind of ['eval','navigate','workflow']) {
    await measure('wok warm subprocess',kind,async()=>{
      if(kind==='eval') assert.equal(await wok('eval','document.title'),'Wok benchmark');
      else if(kind==='navigate') await wok('open',url);
      else {await wok('open',url);await wok('fill','#name','wok benchmark');await wok('click','#go');assert.equal(await wok('get','text','#result'),'wok benchmark');}
    });
    for(const mode of ['warm','cold']) {
      await measure(`playwright ${mode} subprocess`,kind,async()=>{
        const {stdout}=await execFile(process.execPath,[join(here,'playwright-command.mjs'),mode,state.endpoint,kind,url],{env,timeout:30000});
        const result=JSON.parse(stdout);assert.equal(result.value,kind==='workflow'?'wok benchmark':'Wok benchmark');
      },mode==='cold'?1:2);
    }
    persistentPW=await chromium.connectOverCDP(state.endpoint);
    const page=persistentPW.contexts()[0].pages()[0];
    await measure('playwright resident',kind,async()=>{
      assert.equal(await workload(page,kind,url),kind==='workflow'?'wok benchmark':'Wok benchmark');
    });
    await persistentPW.close();persistentPW=null;
  }
  await measure('wok idempotent start','start',()=>wok('start'));
  const finalState=await wok('status');assert.equal(finalState.endpoint,state.endpoint);assert.equal(finalState.pid,state.pid);
  const output={
    recordedAt:new Date().toISOString(),platform:platform(),arch:arch(),cpu:cpus()[0]?.model,cpuThreads:cpus().length,totalMemoryBytes:totalmem(),
    node:process.version,playwright:pwVersion,browser:browserVersion,browserExecutable:relative(root,browserPath).startsWith(".."+sep)?basename(browserPath):relative(root,browserPath),wokBinaryBytes:(await stat(binary)).size,
    samples,wokFirstStartMs:firstStartMs,memory,browserReused:{pid:state.pid,endpointUnchanged:true},rows,
    methodology:[
      'Local fixture served with Cache-Control: no-store. Same headless-shell executable for every mode. No external network page loads.',
      'Subprocess timings include process startup, module loading (Playwright), CDP connection, operation, disconnection, process exit; measured by the parent.',
      'Wok warm uses one CLI invocation for eval/navigation; workflow is four CLI invocations. Playwright workflow executes inside one Node invocation.',
      'Playwright cold launches and closes a new browser per sample; eval also navigates to fixture inside cold timing.',
      'Playwright warm connects to the same persistent browser used by Wok; resident excludes process startup and CDP connection.',
      'Wok browser and render service launched once. Viewer stream is inactive during benchmarks. Browser PID and endpoint checked unchanged at end.',
      'Two unrecorded warmup samples per warm case and one per cold case. Samples are sequential, grouped by mode rather than randomized.',
      'Memory is one idle Linux /proc snapshot, not peak memory. RSS sums shared pages multiple times; PSS apportions shared pages.',
      'Sandbox disabled for all browser launches in this test environment; no claim about production websites or anti-bot behavior.'
    ]
  };
  const jsonPath=join(here,'results.json');await writeFile(jsonPath,JSON.stringify(output,null,2)+'\n');await writeFile(join(here,'benchmark.svg'),renderChart(output));
  const table=rows.map(r=>`| ${r.name} | ${r.workload} | ${r.medianMs.toFixed(2)} | ${r.p95Ms.toFixed(2)} |`).join('\n');
  const report=`# Wok vs Playwright benchmark\n\nRecorded ${output.recordedAt}. ${output.cpu}; ${output.cpuThreads} logical CPUs; ${output.platform}/${output.arch}.\n\nEngine: ${browserVersion}. Playwright ${pwVersion}; Node ${output.node}; ${samples} samples per row.\n\n| Mode | Workload | Median (ms) | p95 (ms) |\n|---|---|---:|---:|\n${table}\n\nWok first start (browser + idle render service): ${firstStartMs.toFixed(2)} ms. Binary: ${(output.wokBinaryBytes/1048576).toFixed(2)} MiB (unstripped).\n\n## Method\n\n${output.methodology.map(x=>'- '+x).join('\n')}\n\n![Wok vs Playwright median latency](benchmark.svg)\n\nMemory snapshot and every raw latency sample: [results.json](results.json).\n\n## Interpretation\n\nWok optimizes shell-driven agent commands and profile reuse. Playwright also supports persistent browser attachment; its resident mode is the useful control for separating lifecycle cost from protocol/action cost. Cold-launch measurements must not be presented as evidence that Playwright always starts cold. This small local fixture does not establish performance on real-world sites.\n`;
  await writeFile(join(here,'RESULTS.md'),report);
  console.log(`Saved ${jsonPath}`);
} finally {
  if(persistentPW) await persistentPW.close().catch(()=>{});
  await wok('stop').catch(()=>{});
  await new Promise(r=>fixture.close(r));
  await rm(home,{recursive:true,force:true});
}
