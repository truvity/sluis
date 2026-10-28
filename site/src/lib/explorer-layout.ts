// Shared by both explorers: the shapes of a map (boxes, lines), the geometry,
// and the routing of the lines. No fact about sluis is stated here.

export type Row = { k: string; v: string; code?: boolean };
export type Detail = {
  title: string;
  kind: string;
  cls: string;
  status?: string;
  od?: string;
  does: string;
  rows: Row[];
  lists: { title: string; items: string[] }[];
  notes: { id?: string; text: string }[];
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
  notes: { text: string; status: string; od?: string }[];
  nodes: MapNode[];
  edges: MapEdge[];
  width: number;
  height: number;
  lanes: { x: number; title: string }[];
};

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
