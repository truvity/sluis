#!/usr/bin/env node
// A real render check: serve site/dist locally, open pages in headless Chrome
// and assert what a reader sees. Static greps (check-built.sh) cannot tell that
// the page script threw before it drew the diagrams; this can.
//
// The pages are discovered, not listed: every built dist/**/index.html that holds
// a `pre.mermaid`, plus the explorer pages. The sweep must find at least
// MIN_DIAGRAM_PAGES pages, and the pages in REQUIRED, so an empty or shrunken
// sweep fails instead of passing on nothing.
//
// For each page, in light and in dark mode:
//   - the page raises no exception and logs no console error;
//   - every `pre.mermaid` ends up holding an <svg> (and the page has as many as
//     its HTML says it has);
//   - every diagram has an Expand button, the diagram text is readable (the
//     computed font size of each SVG label times the SVG's rendered scale is at
//     least MIN_TEXT_PX), and on the first diagram a click on Expand opens the
//     <dialog> with an <svg> in it, which Escape closes;
//   - on the explorer pages, the explorer script ran (the root gets `ex-js`).
//
// Needs Chrome or Chromium (CHROME, or google-chrome / chromium on PATH) and
// Node 22+ (global fetch and WebSocket). No npm dependency.
//
// Chrome is started once, with --remote-debugging-port=0 and a fresh profile, and
// its address is read from the "DevTools listening on ws://..." line it prints on
// stderr. A cold Chrome on a loaded hosted runner can take far longer than a few
// seconds to get that far, so the wait is long (LAUNCH_MS) and bounded; if Chrome
// exits or never says so, its stderr is printed. Only the launch is retried, once,
// never the assertions.
//
// usage: site/scripts/check-render.mjs [dist] [screenshot-dir]
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, extname, join, normalize, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const dist = resolve(root, process.argv[2] ?? 'site/dist');
const shots = process.argv[3] ? resolve(process.argv[3]) : null;

// The explorer pages are listed: they have no diagram to find, and the explorer
// script must have run on them.
const EXPLORERS = ['/architecture/explorer/', '/architecture/audit-explorer/'];
// Pages that must be in the sweep whatever else is found (a diagram on each).
const REQUIRED = ['/concepts/', '/concepts/identity/', '/architecture/context/', '/shapes/local/',
  '/shapes/kubernetes/', '/integrations/connecting/'];
// The fewest pages with a diagram the sweep may find (18 when this was written).
const MIN_DIAGRAM_PAGES = 12;

// Every built page whose HTML holds a pre.mermaid, with how many.
function discover(dist) {
  const found = [];
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const f = join(dir, e.name);
      if (e.isDirectory()) walk(f);
      else if (e.name === 'index.html') {
        const n = (readFileSync(f, 'utf8').match(/<pre\b[^>]*\bclass="[^"]*\bmermaid\b/g) ?? []).length;
        if (n) {
          const rel = relative(dist, dirname(f)).split(sep).join('/');
          found.push({ path: rel ? `/${rel}/` : '/', mermaid: n });
        }
      }
    }
  };
  walk(dist);
  return found.sort((a, b) => a.path.localeCompare(b.path));
}

const SCHEMES = ['light', 'dark'];
const WAIT_MS = 20000;
// How long to wait for Chrome to print its DevTools address, per launch attempt.
const LAUNCH_MS = 60000;
// The smallest on-screen size, in CSS px, of any diagram text. Mermaid fits an
// SVG to its column by default, which shrank a wide sequence diagram to 6 px.
const MIN_TEXT_PX = 11;

const TYPES = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml', '.json': 'application/json', '.txt': 'text/plain; charset=utf-8', '.xml': 'application/xml',
  '.png': 'image/png', '.woff2': 'font/woff2', '.wasm': 'application/wasm' };

function findChrome() {
  const candidates = [process.env.CHROME, 'google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser'].filter(Boolean);
  for (const c of candidates) {
    const r = spawnSync(c, ['--version'], { encoding: 'utf8', timeout: 60000 });
    if (r.status === 0) return c;
  }
  throw new Error('no Chrome or Chromium found: set CHROME, or put google-chrome or chromium on PATH');
}

function serve() {
  const server = createServer((req, res) => {
    let p = normalize(decodeURIComponent(new URL(req.url, 'http://x').pathname));
    let file = join(dist, p);
    if (!file.startsWith(dist)) { res.writeHead(403).end(); return; }
    if (existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html');
    if (!existsSync(file)) { res.writeHead(404).end('not found'); return; }
    res.writeHead(200, { 'content-type': TYPES[extname(file)] ?? 'application/octet-stream' }).end(readFileSync(file));
  });
  return new Promise((ok) => server.listen(0, '127.0.0.1', () => ok(server)));
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Start Chrome and wait for the "DevTools listening on ws://host:port/..." line on
// its stderr. Resolves with the child, the port and the profile directory.
function launchOnce(chrome) {
  const profile = mkdtempSync(join(tmpdir(), 'check-render-'));
  const child = spawn(chrome, ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage', '--hide-scrollbars',
    '--no-first-run', '--no-default-browser-check', '--disable-background-networking', '--disable-extensions',
    '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
  let err = '';
  const t0 = Date.now();
  return new Promise((ok, fail) => {
    const done = (f, v) => { clearTimeout(timer); child.stderr.off('data', onData); f(v); };
    const bad = (why) => {
      child.kill('SIGKILL');
      rmSync(profile, { recursive: true, force: true });
      fail(new Error(`${why}\n--- Chrome stderr (${err.length} bytes) ---\n${err.slice(-4000) || '(empty)'}\n---`));
    };
    const onData = (d) => {
      err += d;
      const m = err.match(/DevTools listening on ws:\/\/[^:/\s]+:(\d+)\//);
      if (m) {
        console.log(`check-render: Chrome up in ${((Date.now() - t0) / 1000).toFixed(1)} s`);
        done(ok, { child, port: m[1], profile });
      }
    };
    const timer = setTimeout(() => bad(`Chrome did not print its DevTools address within ${LAUNCH_MS / 1000} s`), LAUNCH_MS);
    child.stderr.on('data', onData);
    child.on('error', (e) => { clearTimeout(timer); bad(`could not start ${chrome}: ${e.message}`); });
    child.on('exit', (code, sig) => { if (!err.includes('DevTools listening')) { clearTimeout(timer); bad(`Chrome exited early (code ${code}, signal ${sig})`); } });
  });
}

// One retry of the launch, with a fresh profile: a browser that never came up says
// nothing about the page. The assertions are never retried.
async function launch(chrome) {
  try {
    return await launchOnce(chrome);
  } catch (e) {
    console.error(`check-render: Chrome launch failed, retrying once: ${e.message}`);
    return await launchOnce(chrome);
  }
}

// A minimal CDP client over the page's WebSocket.
function connect(url) {
  const ws = new WebSocket(url);
  let id = 0;
  const pending = new Map();
  const listeners = [];
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) {
      const { ok, fail } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? fail(new Error(msg.error.message)) : ok(msg.result);
    } else if (msg.method) listeners.forEach((l) => l(msg));
  };
  const opened = new Promise((ok, fail) => { ws.onopen = ok; ws.onerror = () => fail(new Error('CDP socket failed')); });
  return opened.then(() => ({
    send: (method, params = {}) => new Promise((ok, fail) => { const n = ++id; pending.set(n, { ok, fail }); ws.send(JSON.stringify({ id: n, method, params })); }),
    on: (l) => listeners.push(l),
    clear: () => { listeners.length = 0; },
    close: () => ws.close(),
  }));
}

// Per diagram: whether it holds an svg, whether it has its Expand button, and
// the smallest rendered text size. A label is an SVG <text>, or, in a flowchart
// with HTML labels, an element inside a <foreignObject> that holds a text node
// of its own. Its size on screen is its computed font size times the SVG's
// scale (rendered width over viewBox width).
const STATE = `(() => ({
  pres: [...document.querySelectorAll('pre.mermaid')].map((p) => !!p.querySelector('svg')),
  buttons: [...document.querySelectorAll('pre.mermaid')].map((p) => !!p.parentElement?.querySelector(':scope > button.mz-expand')),
  sizes: [...document.querySelectorAll('pre.mermaid svg')].map((svg) => {
    const vb = svg.viewBox.baseVal;
    const scale = vb && vb.width > 0 ? svg.getBoundingClientRect().width / vb.width : 1;
    const labels = [...svg.querySelectorAll('text, foreignObject *')].filter((el) =>
      el.localName === 'text' ? el.textContent.trim() : [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim()));
    const px = labels.map((el) => parseFloat(getComputedStyle(el).fontSize) * scale);
    return { scale, labels: labels.length, min: px.length ? Math.min(...px) : null };
  }),
  explorer: document.documentElement.classList.contains('ex-js'),
}))()`;

// Click Expand on the first diagram: a <dialog> opens with an svg in it. Then
// Escape (a real key press) must close it and focus must return to the button.
const EXPAND = `(async () => {
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  const btn = document.querySelector('pre.mermaid')?.parentElement?.querySelector(':scope > button.mz-expand');
  if (!btn) return { error: 'no Expand button' };
  btn.focus();
  btn.click();
  await wait(200);
  const dlg = document.querySelector('dialog.mz-dialog');
  const out = { open: !!dlg?.open, svg: !!dlg?.querySelector('svg'), controls: dlg ? dlg.querySelectorAll('button').length : 0 };
  const stage = dlg?.querySelector('.mz-stage');
  out.transform = stage ? stage.style.transform : '';
  return out;
})()`;
const AFTER_ESC = `(() => {
  const dlg = document.querySelector('dialog.mz-dialog');
  const btn = document.querySelector('pre.mermaid')?.parentElement?.querySelector(':scope > button.mz-expand');
  return { open: !!dlg?.open, focusBack: document.activeElement === btn };
})()`;

async function checkPage(cdp, base, page, scheme) {
  const problems = [];
  cdp.clear();
  cdp.on((m) => {
    if (m.method === 'Runtime.exceptionThrown') {
      const d = m.params.exceptionDetails;
      problems.push(`exception: ${d.exception?.description ?? d.text}`);
    } else if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
      problems.push(`console.error: ${m.params.args.map((a) => a.value ?? a.description).join(' ')}`);
    } else if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') {
      problems.push(`log: ${m.params.entry.text} ${m.params.entry.url ?? ''}`);
    }
  });
  await cdp.send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: scheme }] });
  // Chrome starts with a fresh profile, so no stored theme choice overrides the
  // emulated colour scheme.
  await cdp.send('Page.navigate', { url: 'about:blank' });
  await cdp.send('Page.navigate', { url: base + page.path });
  const t0 = Date.now();
  let st;
  for (;;) {
    await sleep(250);
    const r = await cdp.send('Runtime.evaluate', { expression: STATE, returnByValue: true });
    st = r.result.value;
    const drawn = st && st.pres.length >= (page.mermaid ?? 0) && st.pres.every(Boolean) && st.buttons.every(Boolean);
    const ready = page.explorer ? st?.explorer : drawn;
    if (ready) break;
    if (Date.now() - t0 > WAIT_MS) break;
  }
  await sleep(500); // let a late error surface
  if (page.mermaid && (!st || st.pres.length < page.mermaid)) problems.push(`expected ${page.mermaid} pre.mermaid, found ${st ? st.pres.length : 'none'}`);
  if (st && st.pres.some((d) => !d)) problems.push(`${st.pres.filter((d) => !d).length} of ${st.pres.length} pre.mermaid hold no <svg>: the diagram is raw text`);
  if (page.explorer && !st?.explorer) problems.push('the explorer script did not run (no ex-js class on <html>)');
  if (st && st.buttons.some((b) => !b)) problems.push(`${st.buttons.filter((b) => !b).length} of ${st.buttons.length} pre.mermaid have no Expand button`);
  if (st) {
    st.sizes.forEach((z, i) => {
      if (z.min === null) problems.push(`diagram ${i + 1}: found no text to measure`);
      else if (z.min < MIN_TEXT_PX) problems.push(`diagram ${i + 1}: text renders at ${z.min.toFixed(1)} px (scale ${z.scale.toFixed(2)}), below the ${MIN_TEXT_PX} px minimum`);
    });
  }
  if (page.mermaid && st && st.pres.length && st.buttons[0]) {
    const ex = (await cdp.send('Runtime.evaluate', { expression: EXPAND, awaitPromise: true, returnByValue: true })).result.value;
    if (ex.error) problems.push(`expand: ${ex.error}`);
    else {
      if (!ex.open) problems.push('expand: clicking Expand did not open the dialog');
      if (!ex.svg) problems.push('expand: the dialog holds no <svg>');
      if (ex.controls < 4) problems.push(`expand: the dialog has ${ex.controls} buttons, expected zoom in, zoom out, reset and close`);
      if (shots && ex.open) {
        mkdirSync(shots, { recursive: true });
        const png = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
        writeFileSync(join(shots, `mermaid-${page.path.replace(/^\/|\/$/g, '').replace(/\//g, '-')}-${scheme}-expanded.png`), Buffer.from(png.data, 'base64'));
      }
      for (const type of ['keyDown', 'keyUp']) {
        await cdp.send('Input.dispatchKeyEvent', { type, key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27, nativeVirtualKeyCode: 27 });
      }
      await sleep(200);
      const after = (await cdp.send('Runtime.evaluate', { expression: AFTER_ESC, returnByValue: true })).result.value;
      if (after.open) problems.push('expand: Escape did not close the dialog');
      else if (!after.focusBack) problems.push('expand: focus did not return to the Expand button');
    }
  }
  if (shots) {
    const png = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
    mkdirSync(shots, { recursive: true });
    writeFileSync(join(shots, `mermaid-${page.path.replace(/^\/|\/$/g, '').replace(/\//g, '-')}-${scheme}.png`), Buffer.from(png.data, 'base64'));
  }
  return problems;
}

const server = await serve();
const base = `http://127.0.0.1:${server.address().port}`;
let chrome;
let failed = 0;
try {
  const diagramPages = discover(dist);
  const missing = REQUIRED.filter((p) => !diagramPages.some((d) => d.path === p));
  if (diagramPages.length < MIN_DIAGRAM_PAGES || missing.length) {
    console.error(`check-render: FAIL the sweep found ${diagramPages.length} page(s) with a pre.mermaid in ${dist}, expected at least ${MIN_DIAGRAM_PAGES}` +
      (missing.length ? `; missing required: ${missing.join(', ')}` : ''));
    process.exit(1);
  }
  const PAGES = [...diagramPages, ...EXPLORERS.map((path) => ({ path, explorer: true }))];
  const tStart = Date.now();
  chrome = await launch(findChrome());
  const target = await (await fetch(`http://127.0.0.1:${chrome.port}/json/new?about:blank`, { method: 'PUT' })).json();
  const cdp = await connect(target.webSocketDebuggerUrl);
  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');
  await cdp.send('Log.enable');
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  for (const page of PAGES) {
    for (const scheme of SCHEMES) {
      const problems = await checkPage(cdp, base, page, scheme);
      if (problems.length) {
        failed++;
        console.error(`check-render: FAIL ${page.path} (${scheme})`);
        for (const p of problems) console.error(`  ${p}`);
      } else console.log(`check-render: ok   ${page.path} (${scheme})`);
    }
  }
  cdp.close();
  console.log(`check-render: ${PAGES.length} pages (${diagramPages.length} with diagrams, ${EXPLORERS.length} explorers) x ${SCHEMES.length} schemes in ${((Date.now() - tStart) / 1000).toFixed(1)} s`);
} finally {
  chrome?.child.kill('SIGKILL');
  server.close();
  await sleep(200);
  if (chrome) rmSync(chrome.profile, { recursive: true, force: true });
}
if (failed) { console.error(`check-render: ${failed} page render(s) failed`); process.exit(1); }
console.log('check-render: ok');
