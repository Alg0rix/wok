// Real browser verification of the interactive /render page.
import { chromium } from 'playwright-core';
import { execFile as execFileCallback } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, rm, mkdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import { resolve, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';
import assert from 'node:assert/strict';

const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const binary=resolve(process.env.WOK_BIN||join(root,'bin/wok'));
let engine=process.env.WOK_BENCH_BROWSER?resolve(process.env.WOK_BENCH_BROWSER):null;
const home=await mkdtemp(join(tmpdir(),'wok-viewer-'));
const execFile=promisify(execFileCallback);
const cmd=async(...args)=>JSON.parse((await execFile(binary,['--profile','viewer',...args],{env:{...process.env,WOK_HOME:home},timeout:20000})).stdout);
const fixture=createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end(`<!doctype html><title>Wok viewer smoke</title><style>body{font:18px system-ui;padding:40px;background:#f5f2ea;color:#27241f}input,button{font:inherit;padding:12px;margin:10px}</style><h1>Wok live browser</h1><p>Control this page through /render.</p><input id="name" aria-label="Name"><button id="go" onclick="document.querySelector('#result').textContent=document.querySelector('#name').value">Submit</button><p id="result"></p><div style="height:1800px"></div>`)});
await new Promise(r=>fixture.listen(0,'127.0.0.1',r));
let viewer, control;
try {
 if(!engine){const installed=await execFile(binary,['browser','install'],{timeout:300000});engine=JSON.parse(installed.stdout).path;}
 const state=await cmd('start','--browser',engine,'--no-sandbox');
 await cmd('open',`http://127.0.0.1:${fixture.address().port}/`);
 control=await chromium.connectOverCDP(state.endpoint);
 const remotePage=control.contexts()[0].pages()[0];
 const emulation=await remotePage.context().newCDPSession(remotePage);
 await emulation.send('Emulation.setDeviceMetricsOverride',{width:1280,height:720,deviceScaleFactor:2,mobile:false});
 viewer=await chromium.launch({executablePath:engine,headless:true,args:['--no-sandbox']});
 const page=await viewer.newPage({viewport:{width:1440,height:1000}});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(state.renderUrl+'/render');
 await page.waitForFunction(()=>document.querySelector('#screen').naturalWidth>0);
 const point=async(selector)=>{
  const rect=await cmd('eval',`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2,w:innerWidth,h:innerHeight}})()`);
  const image=await page.locator('#screen').boundingBox();
  return {x:rect.x*image.width/rect.w,y:rect.y*image.height/rect.h};
 };
 await page.locator('#screen').click({position:await point('#name')});
 await page.keyboard.type('from viewer');
 const waitValue=async(expression,expected)=>{for(let i=0;i<60;i++){if(await cmd('eval',expression)===expected)return;await new Promise(r=>setTimeout(r,50));}throw Error('Viewer input did not arrive: '+expression);};
 await waitValue(`document.querySelector('#name').value`,'from viewer');
 await page.keyboard.press('Control+a');await page.keyboard.type('Wok works');
 await waitValue(`document.querySelector('#name').value`,'Wok works');
 await page.locator('#screen').click({position:await point('#go')});
 await waitValue(`document.querySelector('#result').textContent`,'Wok works');
 // The image stream remains live while input and CLI clients attach/detach.
 await mkdir(join(root,'docs/evidence'),{recursive:true});
 await page.screenshot({path:join(root,'docs/evidence/render.png'),fullPage:true});
 await page.goto(state.renderUrl+'/render?embed=1');
 await page.waitForFunction(()=>document.querySelector('#screen').naturalWidth>0);
 assert.equal(await page.locator('header').isVisible(),false);
 await page.evaluate(()=>document.body.classList.remove('embed'));
 // Delayed old-tab metrics must not overwrite a newly selected viewport.
 const firstTab=await page.locator('#tabs').inputValue();
 const second=await cmd('tab','new',`http://127.0.0.1:${fixture.address().port}/`);
 await page.route('**/render/viewport?tab='+second.targetId,route=>route.fulfill({contentType:'application/json',body:JSON.stringify({width:640,height:480,dpr:1})}));
 let releaseOld, markSeen;
 const oldReleased=new Promise(r=>releaseOld=r), oldSeen=new Promise(r=>markSeen=r);
 let intercept=true;
 await page.route('**/render/viewport?tab='+firstTab,async route=>{
  if(intercept){intercept=false;markSeen();await oldReleased;await route.fulfill({contentType:'application/json',body:JSON.stringify({width:1280,height:720,dpr:2})});}
  else await route.continue();
 });
 await page.evaluate(()=>refresh());await oldSeen;
 await page.locator('#tabs').selectOption(second.targetId);
 await page.waitForFunction(()=>viewport?.width===640);
 releaseOld();await page.waitForTimeout(100);
 assert.equal(await page.evaluate(()=>viewport.width),640);
 assert.deepEqual(errors,[]);
 console.log('Viewer smoke passed: MJPEG display, coordinate mapping, typing, Ctrl+A, click, high-DPI mapping, embed mode, delayed-tab viewport race; no page errors.');
} finally {
 if(viewer)await viewer.close();
 if(control)await control.close();
 await cmd('stop').catch(()=>{});
 await new Promise(r=>fixture.close(r));
 await rm(home,{recursive:true,force:true});
}
