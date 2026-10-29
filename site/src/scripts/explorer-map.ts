// The explorers' interaction (shared): the shape toggle, node selection,
// and stepping through a flow. Everything it shows is already in the page.
const root = document.documentElement;
const $$ = <T extends Element = HTMLElement>(sel: string, from: ParentNode = document) => Array.from(from.querySelectorAll<T>(sel));

const toggle = document.querySelector<HTMLElement>('[data-ex-toggle]');
const mapRoot = document.querySelector<HTMLElement>('[data-ex-map]');
const KEY = mapRoot?.dataset.storageKey ?? 'sluis-explorer-shape';
const DEFAULT_NODE = mapRoot?.dataset.defaultNode ?? 'entry';
const state = { shape: '', sel: DEFAULT_NODE, mode: 'explore', step: 0 };
const dflt = (toggle?.closest<HTMLElement>('[data-default]') ?? mapRoot)?.dataset.default ?? 'boards';

function activeBlock(): HTMLElement | null {
  return mapRoot ? mapRoot.querySelector<HTMLElement>(`[data-shape-block="${state.shape}"]`) : null;
}

function setShape(id: string) {
  state.shape = id;
  for (const b of $$('[data-shape-block]')) b.hidden = b.dataset.shapeBlock !== id;
  for (const b of $$<HTMLButtonElement>('button[data-shape]', toggle ?? document)) {
    const on = b.dataset.shape === id;
    b.setAttribute('aria-checked', String(on));
    b.tabIndex = on ? 0 : -1;
  }
  for (const r of $$('[data-shape-row]')) r.classList.toggle('ex-hl', r.dataset.shapeRow === id);
  for (const s of $$('.ps')) s.hidden = !(s.dataset.shapes ?? '').split(' ').includes(id);
  try { localStorage.setItem(KEY, id); } catch { /* storage may be unavailable */ }
  render();
}

function render() {
  const block = activeBlock();
  if (!block || !mapRoot) return;
  const nodes = $$<SVGGElement>('.ex-node', block);
  const edges = $$<SVGPathElement>('.ex-edge', block);
  const labels = $$<SVGTextElement>('.ex-elabel', block);
  if (!nodes.some((n) => n.dataset.id === state.sel)) state.sel = DEFAULT_NODE;

  const status = mapRoot.querySelector<HTMLElement>('[data-ex-status]');
  for (const n of nodes) { n.setAttribute('class', n.getAttribute('class')!.split(' ').filter((c) => !/^(sel|nb-out|nb-in|dim|step|done|down)$/.test(c)).join(' ')); n.setAttribute('aria-pressed', 'false'); }
  for (const e of edges) e.setAttribute('class', 'ex-edge');
  for (const l of labels) l.setAttribute('class', 'ex-elabel');
  const byKey = (k: string, arr: Element[]) => arr.filter((x) => (x as HTMLElement).dataset?.key === k || x.getAttribute('data-key') === k);

  if (state.mode === 'explore') {
    const sel = state.sel;
    const nb = new Map<string, string>();
    for (const e of edges) {
      const f = e.getAttribute('data-from')!, t = e.getAttribute('data-to')!;
      if (f !== sel && t !== sel) { e.classList.add('hide'); continue; }
      const out = f === sel;
      e.classList.add(out ? 'out' : 'in');
      for (const l of byKey(e.getAttribute('data-key')!, labels)) if (l.getAttribute('data-end') === (out ? 't' : 'f')) l.classList.add('on');
      const other = out ? t : f;
      nb.set(other, (nb.get(other) ?? '') + (out ? 'o' : 'i'));
    }
    for (const n of nodes) {
      const id = n.dataset.id!;
      if (id === sel) { n.classList.add('sel'); n.setAttribute('aria-pressed', 'true'); }
      else if (nb.has(id)) n.classList.add(nb.get(id)!.includes('o') ? 'nb-out' : 'nb-in');
      else n.classList.add('dim');
    }
    for (const a of $$('[data-detail]', block)) a.hidden = a.dataset.detail !== sel;
    const title = block.querySelector<HTMLElement>(`[data-detail="${sel}"] h3`)?.textContent ?? sel;
    if (status) status.textContent = `${title}: calls it makes are blue, calls it receives orange. Press a box to select it.`;
  } else {
    const flow = mapRoot.querySelector<HTMLElement>(`[data-flow="${state.mode}"]`);
    const steps = flow ? $$('[data-step]', flow) : [];
    const cur = steps[state.step];
    const nowNodes = new Set((cur?.dataset.nodes ?? '').split(' ').filter(Boolean));
    const nowEdges = new Set((cur?.dataset.edges ?? '').split(' ').filter(Boolean));
    const down = new Set((cur?.dataset.down ?? '').split(' ').filter(Boolean));
    const doneNodes = new Set<string>(), doneEdges = new Set<string>();
    steps.slice(0, state.step).forEach((s) => {
      (s.dataset.nodes ?? '').split(' ').filter(Boolean).forEach((x) => doneNodes.add(x));
      (s.dataset.edges ?? '').split(' ').filter(Boolean).forEach((x) => doneEdges.add(x));
    });
    for (const e of edges) {
      const k = e.getAttribute('data-key')!;
      if (nowEdges.has(k)) { e.classList.add('step'); for (const l of byKey(k, labels)) if (l.getAttribute('data-end') === 't') l.classList.add('on'); }
      else if (doneEdges.has(k)) e.classList.add('done');
      else e.classList.add('hide');
    }
    for (const n of nodes) {
      const id = n.dataset.id!;
      if (nowNodes.has(id)) n.classList.add('step');
      else if (doneNodes.has(id)) n.classList.add('done');
      else n.classList.add('dim');
      if (down.has(id)) n.classList.add('down');
    }
    steps.forEach((s, i) => s.classList.toggle('on', i === state.step));
    for (const b of $$<HTMLButtonElement>('[data-nav]', flow ?? document)) b.disabled = (b.dataset.nav === 'prev' && state.step === 0) || (b.dataset.nav === 'next' && state.step >= steps.length - 1);
    if (status) status.textContent = cur ? `Step ${state.step + 1} of ${steps.length}: ${cur.querySelector('button')?.textContent ?? ''}` : '';
  }
}

function setMode(mode: string) {
  state.mode = mode;
  state.step = 0;
  for (const t of $$<HTMLButtonElement>('[data-ex-tabs] [data-mode]')) t.setAttribute('aria-selected', String(t.dataset.mode === mode));
  for (const f of $$('[data-flow]')) f.hidden = f.dataset.flow !== mode;
  for (const b of $$('[data-ex-details]')) b.hidden = mode !== 'explore';
  render();
}

function select(id: string) {
  state.sel = id;
  if (state.mode !== 'explore') setMode('explore'); else render();
  const panel = activeBlock()?.querySelector<HTMLElement>('[data-ex-details]');
  if (panel) {
    const r = panel.getBoundingClientRect();
    if (r.top > window.innerHeight - 80) panel.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }
}

if (mapRoot || toggle) {
  root.classList.add('ex-js');
  for (const el of $$('[data-ex-toggle], [data-ex-tabs], [data-ex-status]')) el.hidden = false;
  let saved = '';
  try { saved = localStorage.getItem(KEY) ?? ''; } catch { /* ignore */ }
  const known = new Set($$('button[data-shape]').map((b) => b.dataset.shape));
  // The shape can also be given in the URL, as ?shape=T2.
  const fromUrl = new URLSearchParams(location.search).get('shape') ?? '';
  const start = known.has(fromUrl) ? fromUrl : known.has(saved) ? saved : dflt;

  toggle?.addEventListener('click', (ev) => {
    const b = (ev.target as HTMLElement).closest<HTMLButtonElement>('button[data-shape]');
    if (b) setShape(b.dataset.shape!);
  });
  toggle?.addEventListener('keydown', (ev) => {
    const keys = ['ArrowRight', 'ArrowDown', 'ArrowLeft', 'ArrowUp'];
    if (!keys.includes(ev.key)) return;
    ev.preventDefault();
    const bs = $$<HTMLButtonElement>('button[data-shape]', toggle);
    const i = bs.findIndex((b) => b.dataset.shape === state.shape);
    const j = (i + (ev.key === 'ArrowRight' || ev.key === 'ArrowDown' ? 1 : bs.length - 1)) % bs.length;
    setShape(bs[j].dataset.shape!);
    bs[j].focus();
  });
  mapRoot?.addEventListener('click', (ev) => {
    const t = ev.target as Element;
    const node = t.closest<SVGGElement>('.ex-node');
    if (node) return select(node.dataset.id!);
    const tab = t.closest<HTMLButtonElement>('[data-ex-tabs] [data-mode]');
    if (tab) return setMode(tab.dataset.mode!);
    const step = t.closest<HTMLElement>('[data-step]');
    if (step && t.closest('button')) { state.step = Number(step.dataset.step); return render(); }
    const nav = t.closest<HTMLButtonElement>('[data-nav]');
    if (nav) { state.step += nav.dataset.nav === 'next' ? 1 : -1; return render(); }
  });
  mapRoot?.addEventListener('keydown', (ev) => {
    const node = (ev.target as Element).closest<SVGGElement>('.ex-node');
    if (node && (ev.key === 'Enter' || ev.key === ' ')) { ev.preventDefault(); select(node.dataset.id!); }
  });
  setMode('explore');
  setShape(start);
}
