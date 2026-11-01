// Shared by both explorers: the shapes of a map (boxes, lines), the geometry,
// the routing of the lines, and the status check. No fact about sluis is
// stated here.

export type Row = { k: string; v: string; code?: boolean; href?: string };
export type Detail = {
  title: string;
  kind: string;
  cls: string;
  status?: string;
  issue?: number;
  does: string;
  rows: Row[];
  lists: { title: string; items: string[] }[];
  notes: { text: string; status?: string; issue?: number }[];
};
export type MapNode = {
  id: string;
  lane: number;
  row: number;
  cls: string;
  shared: boolean;
  title: string;
  l2: string;
  l3: string;
  x: number;
  y: number;
  detail: Detail;
};
export type MapEdge = { key: string; f: string; t: string; m: string; d: string; path: string; tx: number; ty: number; fx: number; fy: number };
export type MapModel = {
  id: string;
  name: string;
  intro: string;
  notes: { text: string; status: string; issue?: number }[];
  nodes: MapNode[];
  edges: MapEdge[];
  width: number;
  height: number;
  lanes: { x: number; title: string }[];
};

// Status. An item of the data files is `decided` or `proposed`. A proposed item
// names its open tracking issue (`issue: <n>`, an issue of truvity/sluis), and
// the chip links to it. A decided item names no issue: it is stated as fact.
export const ISSUES = 'https://github.com/truvity/sluis/issues/';
export const issueUrl = (n: number) => `${ISSUES}${n}`;

/**
 * Fails the build when an item of a data file has a status that is not a key of
 * `statuses`, when a proposed item names no issue, or when a decided item names
 * one. Walks the whole parsed file, so every `status:` is checked wherever it is.
 */
export function assertStatuses(tree: unknown, statuses: Record<string, string>, file: string, path = ''): void {
  if (Array.isArray(tree)) {
    tree.forEach((x, i) => assertStatuses(x, statuses, file, `${path}[${i}]`));
    return;
  }
  if (!tree || typeof tree !== 'object') return;
  const o = tree as Record<string, unknown>;
  const at = `explorer: ${file}${path ? ` at ${path}` : ''}`;
  if ('status' in o) {
    const s = o.status;
    if (typeof s !== 'string' || !(s in statuses)) throw new Error(`${at}: status "${String(s)}" is not one of ${Object.keys(statuses).join(', ')}`);
    if (s === 'proposed' && !(Number.isInteger(o.issue) && (o.issue as number) > 0)) throw new Error(`${at}: a proposed item needs \`issue: <n>\`, its open tracking issue`);
    if (s !== 'proposed' && o.issue != null) throw new Error(`${at}: only a proposed item names an issue; a ${s} item is stated as fact`);
  } else if (o.issue != null) {
    throw new Error(`${at}: \`issue\` without \`status: proposed\``);
  }
  for (const [k, v] of Object.entries(o)) if (k !== 'statuses') assertStatuses(v, statuses, file, path ? `${path}.${k}` : k);
}

export const W = 170;
export const H = 58;
export const GAP = 38;
export const PITCH = 66;
export const Y0 = 46;
export const laneX = (lane: number) => 12 + lane * (W + GAP);

export const fill = (tpl: string, vars: Record<string, string>) => tpl.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
export const label = (x: string) => x.charAt(0).toUpperCase() + x.slice(1);

export const place = (n: Omit<MapNode, 'x' | 'y'>): MapNode => ({ ...n, x: laneX(n.lane), y: Math.round(Y0 + n.row * PITCH) });

export function size(nodes: MapNode[], laneTitles: string[]) {
  const lastRow = Math.max(...nodes.map((n) => n.row));
  return {
    height: Math.round(Y0 + lastRow * PITCH + H + 18),
    width: laneX(laneTitles.length - 1) + W + 12,
    lanes: laneTitles.map((title, i) => ({ x: laneX(i), title })),
  };
}

/** Routes each edge `{f, t, m, d}` between two placed nodes. `labelOf` may rewrite a label. */
export function routeEdges(nodes: MapNode[], live: { f: string; t: string; m: string; d: string; dash?: boolean }[], labelOf?: (e: { f: string; t: string; m: string }) => string): MapEdge[] {
  const byId = new Map(nodes.map((n) => [n.id, n] as const));
  const sideOf = (a: MapNode, b: MapNode) => (b.lane > a.lane ? 'r' : b.lane < a.lane ? 'l' : b.y > a.y ? 'b' : 't');
  // Spread the attachment points along a side so that lines do not overlap.
  const want = new Map<string, string[]>();
  for (const e of live) {
    const a = byId.get(e.f)!, b = byId.get(e.t)!;
    const key = `${e.f}>${e.t}`;
    for (const k of [`${e.f}:${sideOf(a, b)}`, `${e.t}:${sideOf(b, a)}`]) {
      if (!want.has(k)) want.set(k, []);
      want.get(k)!.push(key);
    }
  }
  const slots = new Map<string, number>();
  for (const [k, list] of want) list.forEach((key, i) => slots.set(`${k}|${key}`, (i + 1) / (list.length + 1)));
  const attach = (n: MapNode, side: string, key: string): [number, number] => {
    const s = slots.get(`${n.id}:${side}|${key}`) ?? 0.5;
    if (side === 'r') return [n.x + W, n.y + H * s];
    if (side === 'l') return [n.x, n.y + H * s];
    if (side === 'b') return [n.x + W * s, n.y + H];
    return [n.x + W * s, n.y];
  };
  return live.map((e) => {
    const a = byId.get(e.f)!, b = byId.get(e.t)!;
    const key = `${e.f}>${e.t}`;
    const [x1, y1] = attach(a, sideOf(a, b), key);
    const [x2, y2] = attach(b, sideOf(b, a), key);
    const mx = (x1 + x2) / 2;
    const path = a.lane === b.lane ? `M${x1} ${y1}L${x2} ${y2}` : `M${x1} ${y1}C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}`;
    const m = labelOf ? labelOf(e) : e.m;
    return { key, f: e.f, t: e.t, m, d: e.d, path, tx: b.x + 8, ty: b.y - 4, fx: a.x + 8, fy: a.y - 4 };
  });
}
