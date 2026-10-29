// Builds the audit explorer's model, per preset, from src/data/explorer/audit.yaml.
// Ports, drivers and runtimes come from architecture.yaml so the two explorers
// agree. Nothing here states a fact about audit. A "shape" in this file is a
// preset (T0 to T6) of an environment; a "role" is an actor.
import { parse } from 'yaml';
import { assertOpenDecision } from './od';
import raw from '../data/explorer/audit.yaml?raw';
import { data as arch } from './explorer-arch';
import { fill, place, routeEdges, size, label, type Detail, type MapModel, type MapNode, type Row } from './explorer-layout';

/* eslint-disable @typescript-eslint/no-explicit-any */
export const data: any = parse(raw);
for (const m of raw.matchAll(/\bOD-\d+\b/g)) assertOpenDecision(m[0]);
export const statuses: Record<string, string> = arch.statuses;

export type AuditShape = MapModel & {
  shape: any;
  audit: any;
  transport: any;
  ack: string;
  wait: string;
  recv: string;
  drain: string;
  bound: string;
};

export function shapes(): AuditShape[] {
  return arch.shapes.map(build);
}

function build(sh: any): AuditShape {
  const rt = arch.runtimes[sh.runtime];
  const au = data.shapes[sh.id];
  const ok = data.transport_kinds[sh.ports.transport === 'sqs' ? 'sqs' : 'spool'];
  const cluster = sh.nodes > 1;
  const lambda = sh.runtime === 'lambda';
  const nodes: MapNode[] = [];
  const nameOf: Record<string, string> = {};
  const defs = data.nodes;

    for (const [id, n] of Object.entries<any>(defs)) {
    let l2 = n.l2 ?? '';
    let l3 = n.l3 ?? '';
    let shared = !!n.shared;
    let cls = n.cls as string;
    let rows: Row[] = [];
    let kind = '';
    let status: string | undefined;
    let od: string | undefined;
    const notes: Detail['notes'] = [];

    if (n.role) {
      const rid = n.role;
      if (lambda && n.l2_lambda) { l2 = n.l2_lambda; l3 = n.l3_lambda; }
      else if (lambda && n.l3_lambda) { l3 = n.l3_lambda; }
      else if (!n.l2 || n.role === 'entry') {
        if (n.role !== 'entry') { l2 = fill(rt.role.id_short, { id: rid }); l3 = fill(rt.role.sock_short, { id: rid }); }
      }
      kind = 'actor';
      status = 'agreed';
      rows = [
        { k: 'Unit', v: fill(rt.role.unit, { id: rid }), code: true },
        { k: 'Identity', v: fill(rt.role.identity, { id: rid }), code: true },
        { k: 'Listens', v: rid === 'entry' ? (lambda ? 'TCP 127.0.0.1:8080 (an example port) inside the sandbox, through the Lambda Web Adapter' : 'TCP 127.0.0.1:8080 (an example port), loopback only') : fill(rt.role.listen, { id: rid }), code: true },
      ];
      if (rid === 'notary') rows.push({ k: 'Seal key', v: au.notary_key });
    } else if (n.port && cls === 'driver') {
      const did = sh.ports[n.port];
      const d = arch.drivers[did];
      const perRole = d.instance === 'per-actor';
      shared = !perRole;
      cls = perRole ? 'driver-role' : 'driver-shared';
      const portTitle = label(n.port);
      l2 = `${d.title}, ${d.instance}`;
      l3 = `${portTitle} port`;
      kind = perRole ? 'driver service, per-actor' : `driver service, shared${d.shared_name ? ` (${d.shared_name})` : ''}`;
      status = d.status;
      od = d.od;
      rows = [
        { k: 'Driver', v: `${d.title}, instance: ${d.instance}` },
        { k: 'Backed by', v: d.backing },
      ];
      if (!perRole) {
        const tpl = rt.shared_driver;
        rows.push({ k: 'Unit', v: fill(tpl.unit, { driver: did, id: did }), code: true });
        rows.push({ k: 'Listens', v: fill(tpl.listen, { driver: did, id: did }), code: true });
      } else {
        rows.push({ k: 'Listens', v: `${fill(rt.role_driver.listen, { driver: 'aws', role: '<role>' })}, one per role that uses it`, code: true });
      }
      if (n.port === 'transport') {
        rows.push({ k: 'Durable means', v: ok.durable });
        rows.push({ k: 'Records wait', v: ok.wait + (cluster ? ok.cluster_wait ?? '' : '') });
        rows.push({ k: 'Who the record is from', v: ok.caller });
        rows.push({ k: 'Instance', v: ok.instance });
        rows.push({ k: 'Mode in this preset', v: ok.mode });
      }
      if (n.port === 'keys') rows.push({ k: 'Used here by', v: 'the notary only' });
    } else if (n.port && cls === 'store') {
      const did = sh.ports[n.port];
      const d = arch.drivers[did];
      const bl = arch.backing_lines[`${n.port}.${did}`] ?? [d.backing, ''];
      l2 = bl[0];
      l3 = bl[1] || n.title;
      kind = 'backing store';
      status = n.port === 'index' ? d.status : 'agreed';
      od = n.port === 'index' ? d.od : undefined;
      rows = [{ k: 'Backend', v: d.backing }];
      if (n.port === 'blob') {
        rows = [{ k: 'Backend', v: au.blob }, { k: 'Lock', v: `${au.lock} (which installs, and which mode, is not yet decided)` }, { k: 'Prefixes', v: 'batch objects, seals/, keys/ (the trust log)' }];
      }
      if (n.port === 'keys') rows.push({ k: 'Notary seal key', v: au.notary_key });
      if (n.port === 'state') rows = [{ k: 'Backend', v: sh.state }, { k: 'Holds', v: 'audit_batches, cursors and dedupe, replicated; no record bodies' }];
      if (n.port === 'index') {
        rows = [{ k: 'Backend', v: d.backing }, { k: 'Placement', v: sh.runtime === 'lambda' ? 'None by default (proposed); a postgres index through RDS Proxy is allowed, and otherwise a separate audit install holds the search index' : 'One file per node in the current draft; where Search runs is derived from the port and driver rules' }];
      }
    } else if (cls === 'external') {
      kind = 'outside sluis';
    } else if (id === 'producers') {
      kind = 'actors, producers';
      status = 'agreed';
    } else if (id === 'recovery') {
      kind = 'break-glass tool';
      status = 'agreed';
    } else if (id === 'rspool') {
      kind = 'local spool, break-glass';
      status = 'agreed';
    }
    if (id === 'index' && !n.port) rows = [];
    // Optional per-node overrides from the YAML (used by actors whose placement is open).
    if (n.kind) kind = n.kind;
    if (n.status) status = n.status;
    if (n.od) od = n.od;
    if (n.rows) rows = n.rows;
    if (n.extra_rows) rows = [...rows, ...n.extra_rows];
    for (const o of n.od_note ?? []) notes.push({ id: o.id, text: o.text });

    const detail: Detail = {
      title: n.title,
      kind: kind || n.cls,
      cls: cls === 'driver' ? 'driver-shared' : cls,
      status, od,
      does: n.does,
      rows: [...rows, ...(n.holds ? [{ k: 'Holds', v: n.holds }] : []), ...(n.never ? [{ k: 'Never holds', v: n.never }] : [])],
      lists: [],
      notes,
    };
    if (id === 'index') {
      // The index node is an index store, not a driver.
      const d = arch.drivers[sh.ports.index];
      const bl = arch.backing_lines[`index.${sh.ports.index}`];
      nodes.push(place({ id, lane: n.lane, row: n.row, cls: 'store', shared: false, title: 'search index', l2: bl[0], l3: bl[1], detail: { ...detail, kind: 'search index', cls: 'store', status: d.status, od: d.od, rows: [{ k: 'Backend', v: d.backing }, { k: 'Placement', v: lambda ? 'none by default (proposed); a postgres index through RDS Proxy is allowed, and otherwise a separate audit install holds the search index' : 'one file per node in the current draft; where Search runs is derived from the port and driver rules' }], notes: [] } }));
      nameOf[id] = 'search index';
      continue;
    }
    nodes.push(place({ id, lane: n.lane, row: n.row, cls: detail.cls, shared, title: n.title, l2, l3, detail }));
    nameOf[id] = n.title;
  }

  const ids = new Set(nodes.map((x) => x.id));
  const live = data.calls.filter((e: any) => ids.has(e.f) && ids.has(e.t));
  const edges = routeEdges(nodes, live.map((e: any) => ({ ...e })));
  const nm = (id: string) => nameOf[id] ?? id;
  for (const n of nodes) {
    if (n.cls === 'external') continue;
    const outs = edges.filter((e) => e.f === n.id && e.m);
    const ins = edges.filter((e) => e.t === n.id && e.m);
    if (outs.length) n.detail.lists.push({ title: 'Calls', items: outs.map((e) => `${nm(e.t)}: ${e.m}${e.d ? `. ${e.d}` : ''}`) });
    if (ins.length) n.detail.lists.push({ title: 'Called by', items: ins.map((e) => `${nm(e.f)}: ${e.m}`) });
  }

  const { width, height, lanes } = size(nodes, data.lanes);
  const noteList = [];
  if (au.note) noteList.push({ text: au.note, status: au.assumed ? 'proposed' : 'agreed' });
  return {
    id: sh.id, name: sh.name, nodes, edges, width, height, lanes,
    intro: `Environment: ${sh.environment}. Transport: ${ok.short}. Keys: ${sh.keys}. State: ${sh.state}. Blob: ${au.blob}, ${au.lock}.`,
    notes: noteList.map((x) => (x.od ? x : { ...x, od: undefined })) as any,
    shape: sh, audit: au, transport: ok,
    ack: ok.durable,
    wait: ok.wait + (cluster ? ok.cluster_wait ?? '' : ''),
    recv: ok.recv + (cluster ? ok.cluster_recv ?? '' : ''),
    drain: ok.drain + (cluster ? ok.cluster_drain ?? '' : ''),
    bound: ok.bound,
  };
}
