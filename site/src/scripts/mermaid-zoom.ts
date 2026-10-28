// An "Expand" button on each rendered Mermaid diagram, and the viewer it opens:
// a modal <dialog> holding a copy of the SVG that can be zoomed (wheel, pinch,
// + / - buttons or keys) and panned (drag, arrow keys), fitted to the viewport
// on open. No dependency.
//
// astro-mermaid draws each diagram into `pre.mermaid` and draws it again (it
// replaces the pre's content) when the theme changes. The pre is therefore
// wrapped once in `.mz-wrap`, and the button lives beside it, so it survives
// a redraw. A MutationObserver wraps diagrams as they appear.

const MIN_FIT = 0.05; // smallest scale, as a multiple of the fit scale
const MAX_ABS = 24; // largest absolute scale
const FIT_CAP = 3; // do not blow a small diagram up beyond this when fitting
const STEP = 1.25; // one + or - press

let dialog: HTMLDialogElement | null = null;
let opener: HTMLElement | null = null;

function button(label: string, text: string, cls = ''): HTMLButtonElement {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = `mz-btn ${cls}`.trim();
  b.textContent = text;
  b.setAttribute('aria-label', label);
  b.title = label;
  return b;
}

function naturalSize(svg: SVGSVGElement): { w: number; h: number } {
  const vb = svg.viewBox.baseVal;
  if (vb && vb.width > 0 && vb.height > 0) return { w: vb.width, h: vb.height };
  const r = svg.getBoundingClientRect();
  return { w: r.width || 300, h: r.height || 150 };
}

class Viewer {
  readonly dlg: HTMLDialogElement;
  private readonly view: HTMLDivElement;
  private readonly stage: HTMLDivElement;
  private readonly label: HTMLSpanElement;
  private scale = 1;
  private fit = 1;
  private x = 0;
  private y = 0;
  private w = 1;
  private h = 1;
  private readonly pointers = new Map<number, { x: number; y: number }>();
  private pinch: { dist: number } | null = null;

  constructor() {
    const dlg = document.createElement('dialog');
    dlg.className = 'mz-dialog';
    dlg.setAttribute('aria-label', 'Diagram, expanded');

    const bar = document.createElement('div');
    bar.className = 'mz-bar';
    const zoomIn = button('Zoom in', '+');
    const zoomOut = button('Zoom out', '−');
    const reset = button('Reset zoom to fit', 'Reset');
    this.label = document.createElement('span');
    this.label.className = 'mz-spacer';
    this.label.setAttribute('aria-live', 'polite');
    this.label.style.fontSize = 'var(--sl-text-sm)';
    this.label.style.paddingInline = '0.5rem';
    const close = button('Close', 'Close', 'mz-close');
    bar.append(zoomOut, zoomIn, reset, this.label, close);

    this.view = document.createElement('div');
    this.view.className = 'mz-view';
    this.view.tabIndex = 0;
    this.view.setAttribute('role', 'application');
    this.view.setAttribute(
      'aria-label',
      'Diagram viewer. Plus and minus zoom, arrow keys move, zero resets, Escape closes.',
    );
    this.stage = document.createElement('div');
    this.stage.className = 'mz-stage';
    this.view.append(this.stage);
    dlg.append(bar, this.view);
    document.body.append(dlg);
    this.dlg = dlg;

    zoomIn.addEventListener('click', () => this.zoomAtCentre(STEP));
    zoomOut.addEventListener('click', () => this.zoomAtCentre(1 / STEP));
    reset.addEventListener('click', () => this.fitToView());
    close.addEventListener('click', () => dlg.close());
    // A click on the backdrop is a click on the dialog element itself.
    dlg.addEventListener('click', (e) => {
      if (e.target === dlg) dlg.close();
    });
    dlg.addEventListener('close', () => {
      this.stage.replaceChildren();
      this.pointers.clear();
      this.pinch = null;
      const back = opener;
      opener = null;
      back?.focus();
    });

    this.view.addEventListener('wheel', (e) => this.onWheel(e), { passive: false });
    this.view.addEventListener('pointerdown', (e) => this.onDown(e));
    this.view.addEventListener('pointermove', (e) => this.onMove(e));
    this.view.addEventListener('pointerup', (e) => this.onUp(e));
    this.view.addEventListener('pointercancel', (e) => this.onUp(e));
    this.view.addEventListener('keydown', (e) => this.onKey(e));
    this.view.addEventListener('dblclick', (e) => {
      const p = this.local(e.clientX, e.clientY);
      this.zoomAt(p.x, p.y, 2);
    });
    window.addEventListener('resize', () => {
      if (dlg.open) this.fitToView();
    });
  }

  open(svg: SVGSVGElement, from: HTMLElement): void {
    opener = from;
    const copy = svg.cloneNode(true) as SVGSVGElement;
    const { w, h } = naturalSize(svg);
    this.w = w;
    this.h = h;
    copy.style.maxWidth = 'none';
    copy.setAttribute('width', String(w));
    copy.setAttribute('height', String(h));
    // The id stays: the <style> inside the SVG scopes its rules to `#<id>`, and a
    // CSS id selector matches the copy as well as the original.
    this.stage.replaceChildren(copy);
    this.dlg.showModal();
    this.fitToView();
    this.view.focus();
  }

  private fitToView(): void {
    const vw = this.view.clientWidth;
    const vh = this.view.clientHeight;
    const pad = 16;
    this.fit = Math.min(FIT_CAP, Math.max(0.01, Math.min((vw - pad * 2) / this.w, (vh - pad * 2) / this.h)));
    this.scale = this.fit;
    this.x = (vw - this.w * this.scale) / 2;
    this.y = (vh - this.h * this.scale) / 2;
    this.apply();
  }

  private apply(): void {
    this.stage.style.transform = `translate(${this.x}px, ${this.y}px) scale(${this.scale})`;
    this.label.textContent = `${Math.round(this.scale * 100)}%`;
  }

  private local(cx: number, cy: number): { x: number; y: number } {
    const r = this.view.getBoundingClientRect();
    return { x: cx - r.left, y: cy - r.top };
  }

  // Zoom by `factor`, keeping the point (px, py) of the view where it is.
  private zoomAt(px: number, py: number, factor: number): void {
    const next = Math.min(MAX_ABS, Math.max(this.fit * MIN_FIT, this.scale * factor));
    const k = next / this.scale;
    this.x = px - (px - this.x) * k;
    this.y = py - (py - this.y) * k;
    this.scale = next;
    this.apply();
  }

  private zoomAtCentre(factor: number): void {
    this.zoomAt(this.view.clientWidth / 2, this.view.clientHeight / 2, factor);
  }

  private onWheel(e: WheelEvent): void {
    e.preventDefault();
    const p = this.local(e.clientX, e.clientY);
    // A trackpad pinch arrives as a wheel event with ctrlKey and small deltas.
    const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
    const k = e.ctrlKey ? 0.01 : 0.0018;
    this.zoomAt(p.x, p.y, Math.exp(-e.deltaY * unit * k));
  }

  private onDown(e: PointerEvent): void {
    this.view.setPointerCapture(e.pointerId);
    this.pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    this.view.classList.add('mz-dragging');
    this.pinch = this.pointers.size === 2 ? { dist: this.spread() } : null;
  }

  private onMove(e: PointerEvent): void {
    const prev = this.pointers.get(e.pointerId);
    if (!prev) return;
    const cur = { x: e.clientX, y: e.clientY };
    if (this.pointers.size === 1) {
      this.x += cur.x - prev.x;
      this.y += cur.y - prev.y;
      this.pointers.set(e.pointerId, cur);
      this.apply();
      return;
    }
    // Two fingers: the midpoint pans, the change of distance zooms.
    const before = this.midpoint();
    this.pointers.set(e.pointerId, cur);
    const after = this.midpoint();
    this.x += after.x - before.x;
    this.y += after.y - before.y;
    const dist = this.spread();
    if (this.pinch && this.pinch.dist > 0 && dist > 0) {
      const p = this.local(after.x, after.y);
      this.zoomAt(p.x, p.y, dist / this.pinch.dist);
    }
    this.pinch = { dist };
    this.apply();
  }

  private onUp(e: PointerEvent): void {
    this.pointers.delete(e.pointerId);
    this.pinch = this.pointers.size === 2 ? { dist: this.spread() } : null;
    if (this.pointers.size === 0) this.view.classList.remove('mz-dragging');
  }

  private midpoint(): { x: number; y: number } {
    const pts = [...this.pointers.values()];
    return {
      x: pts.reduce((a, p) => a + p.x, 0) / pts.length,
      y: pts.reduce((a, p) => a + p.y, 0) / pts.length,
    };
  }

  private spread(): number {
    const [a, b] = [...this.pointers.values()];
    return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : 0;
  }

  private onKey(e: KeyboardEvent): void {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const move = 60;
    const keys: Record<string, () => void> = {
      '+': () => this.zoomAtCentre(STEP),
      '=': () => this.zoomAtCentre(STEP),
      '-': () => this.zoomAtCentre(1 / STEP),
      _: () => this.zoomAtCentre(1 / STEP),
      '0': () => this.fitToView(),
      ArrowLeft: () => { this.x += move; this.apply(); },
      ArrowRight: () => { this.x -= move; this.apply(); },
      ArrowUp: () => { this.y += move; this.apply(); },
      ArrowDown: () => { this.y -= move; this.apply(); },
    };
    const act = keys[e.key];
    if (act) {
      e.preventDefault();
      act();
    }
  }
}

let viewer: Viewer | null = null;

function enhance(pre: HTMLElement): void {
  if (pre.parentElement?.classList.contains('mz-wrap')) return;
  if (!pre.querySelector('svg')) return; // not drawn yet, or an error box
  const wrap = document.createElement('div');
  wrap.className = 'mz-wrap';
  pre.replaceWith(wrap);
  const expand = button('Expand diagram', 'Expand', 'mz-expand');
  expand.addEventListener('click', () => {
    const svg = pre.querySelector<SVGSVGElement>('svg');
    if (!svg) return;
    viewer ??= new Viewer();
    viewer.open(svg, expand);
  });
  wrap.append(expand, pre);
}

function scan(): void {
  document.querySelectorAll<HTMLElement>('pre.mermaid').forEach(enhance);
}

let queued = false;
const observer = new MutationObserver(() => {
  if (queued) return;
  queued = true;
  requestAnimationFrame(() => {
    queued = false;
    scan();
  });
});

function start(): void {
  if (!document.querySelector('pre.mermaid')) return;
  scan();
  // A diagram appears when astro-mermaid replaces the pre's text with an <svg>.
  document.querySelectorAll('pre.mermaid').forEach((pre) => observer.observe(pre, { childList: true }));
}

start();
