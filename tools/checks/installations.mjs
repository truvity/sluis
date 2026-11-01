#!/usr/bin/env node
// The example installations under tests/installations/<name>/, against the rules that
// docs/reference/install-file.md and docs/reference/vocabulary.md give `sluis config check`:
//
//   - every upstream of the install file names, in `org`, a boundary of kind `company`;
//   - the vocabulary file that `policy.vocabulary` names has the shape of the vocabulary page:
//     closed keys, names of the allowed set, roles written in the short or the long form, and no
//     implied role that is missing, no cycle in a ladder, no undeclared scope, no role that exists
//     in more scopes than a role it implies;
//   - every grant name in a client's `requires`, in a Cedar policy file and in the JSON files
//     beside the vocabulary (links, test vectors) is a grant the vocabulary declares.
//
// An error names the file and the path, never a value (as `sluis config check` does).
// Needs the `yaml` package of site/ (`just site-install`).
//
// usage: tools/checks/installations.mjs [repo-root]
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { reporter, repoRoot, trackedFiles } from './lib.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const { parse } = createRequire(join(here, '..', '..', 'site', 'package.json'))('yaml');

const root = repoRoot();
const { error, finish } = reporter('check-installations');

const NAME = /^[A-Za-z0-9_-]{1,64}$/;
const SCHEMA_ID = 'https://sluis.id/schema/vocabulary/v1/vocabulary.schema.json';
const isMap = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

function load(path) {
  try {
    return parse(readFileSync(join(root, path), 'utf8'));
  } catch {
    error(`${path}: not valid YAML (or a duplicate key)`);
    return undefined;
  }
}

// The vocabulary file. Returns the set of declared grant names, or undefined when the file is unusable.
function vocabulary(path) {
  const v = load(path);
  if (v === undefined) return undefined;
  let ok = true;
  const bad = (m) => { ok = false; error(`${path}: ${m}`); };
  if (!isMap(v)) { bad('is not a mapping'); return undefined; }
  for (const k of Object.keys(v)) if (!['$schema', 'scopes', 'things'].includes(k)) bad(`${k}: unknown key`);
  if ('$schema' in v && v.$schema !== SCHEMA_ID) bad('$schema: not the vocabulary schema id');
  if (!isMap(v.scopes) || Object.keys(v.scopes).length === 0) { bad('scopes: missing or empty'); return undefined; }
  if (!isMap(v.things) || Object.keys(v.things).length === 0) { bad('things: missing or empty'); return undefined; }
  const scopes = Object.keys(v.scopes);
  for (const [s, d] of Object.entries(v.scopes)) {
    if (!NAME.test(s)) bad(`scopes: a scope name is outside the allowed set (${s.length} characters)`);
    if (!isMap(d)) { bad(`scopes.${s}: not a mapping`); continue; }
    for (const k of Object.keys(d)) if (k !== 'sensitive') bad(`scopes.${s}.${k}: unknown key`);
    if ('sensitive' in d && typeof d.sensitive !== 'boolean') bad(`scopes.${s}.sensitive: not a boolean`);
  }
  const grants = new Set();
  for (const [t, d] of Object.entries(v.things)) {
    if (!NAME.test(t)) bad(`things: a thing name is outside the allowed set (${t.length} characters)`);
    if (!isMap(d)) { bad(`things.${t}: not a mapping`); continue; }
    for (const k of Object.keys(d)) if (k !== 'roles') bad(`things.${t}.${k}: unknown key`);
    if (!isMap(d.roles) || Object.keys(d.roles).length === 0) { bad(`things.${t}.roles: missing or empty`); continue; }
    // role -> { implies, scopes }
    const roles = new Map();
    for (const [r, def] of Object.entries(d.roles)) {
      const at = `things.${t}.roles.${r}`;
      if (!NAME.test(r)) bad(`things.${t}.roles: a role name is outside the allowed set (${r.length} characters)`);
      let implies = [];
      let rs = scopes;
      if (Array.isArray(def)) implies = def;
      else if (isMap(def)) {
        for (const k of Object.keys(def)) if (!['implies', 'scopes'].includes(k)) bad(`${at}.${k}: unknown key`);
        if ('implies' in def) implies = def.implies;
        if ('scopes' in def) {
          rs = def.scopes;
          if (!Array.isArray(rs) || rs.length === 0) { bad(`${at}.scopes: not a non-empty list`); rs = []; }
        }
      } else bad(`${at}: neither a list of roles nor a mapping`);
      if (!Array.isArray(implies) || implies.some((x) => typeof x !== 'string')) { bad(`${at}.implies: not a list of role names`); implies = []; }
      if (new Set(implies).size !== implies.length) bad(`${at}.implies: a role is repeated`);
      if (new Set(rs).size !== rs.length) bad(`${at}.scopes: a scope is repeated`);
      for (const s of rs) if (!scopes.includes(s)) bad(`${at}.scopes: a scope is not declared`);
      roles.set(r, { implies, scopes: rs });
    }
    for (const [r, def] of roles) {
      for (const i of def.implies) {
        const at = `things.${t}.roles.${r}.implies`;
        if (i === r) bad(`${at}: a role implies itself`);
        else if (!roles.has(i)) bad(`${at}: an implied role is not a role of the thing`);
        else if (!def.scopes.every((s) => roles.get(i).scopes.includes(s))) bad(`${at}: the role exists in a scope that an implied role does not`);
      }
    }
    // a cycle in the ladder
    const state = new Map();
    const walk = (r) => {
      if (state.get(r) === 2) return false;
      if (state.get(r) === 1) return true;
      state.set(r, 1);
      const cyc = (roles.get(r)?.implies ?? []).some((i) => roles.has(i) && walk(i));
      state.set(r, 2);
      return cyc;
    };
    if ([...roles.keys()].some((r) => state.get(r) === undefined && walk(r))) bad(`things.${t}.roles: the ladder has a cycle`);
    for (const [r, def] of roles) for (const s of def.scopes) grants.add(`${s}:${t}:${r}`);
  }
  return ok ? grants : undefined;
}

// Every `requires` list below a value.
function* requires(node, path) {
  if (!isMap(node)) return;
  for (const [k, v] of Object.entries(node)) {
    if (k === 'requires' && Array.isArray(v)) yield [`${path}.requires`, v];
    else yield* requires(v, `${path}.${k}`);
  }
}

let checked = 0;
const tracked = trackedFiles(root);
for (const file of tracked.filter((f) => /^tests\/installations\/[^/]+\/install\.yaml$/.test(f))) {
  checked++;
  const doc = load(file);
  if (!isMap(doc)) continue;
  const boundaries = isMap(doc.boundaries) ? doc.boundaries : {};
  for (const [name, u] of Object.entries(isMap(doc.upstreams) ? doc.upstreams : {})) {
    if (!isMap(u) || typeof u.org !== 'string') error(`${file}: upstreams.${name}.org is missing`);
    else if (!isMap(boundaries[u.org]) || boundaries[u.org].kind !== 'company') error(`${file}: upstreams.${name}.org names no boundary of kind company`);
  }
  const policy = isMap(doc.policy) ? doc.policy : {};
  if (typeof policy.vocabulary !== 'string') { error(`${file}: policy.vocabulary is missing`); continue; }
  const dir = dirname(file);
  const vpath = join(dir, policy.vocabulary);
  if (!existsSync(join(root, vpath))) { error(`${file}: policy.vocabulary names a file that does not exist`); continue; }
  const grants = vocabulary(vpath);
  if (!grants) continue;
  const known = (name) => grants.has(name);
  for (const [path, list] of requires(doc, 'install')) {
    for (const [i, g] of list.entries()) if (typeof g !== 'string' || !known(g)) error(`${file}: ${path}[${i}] is not a grant the vocabulary declares`);
  }
  const beside = [];
  for (const f of Array.isArray(policy.files) ? policy.files : []) {
    if (typeof f === 'string' && existsSync(join(root, dir, f))) beside.push(join(dir, f));
    else error(`${file}: policy.files names a file that does not exist`);
  }
  const vdir = dirname(vpath);
  for (const f of readdirSync(join(root, vdir)).sort()) if (f.endsWith('.json')) beside.push(join(vdir, f));
  for (const f of beside) {
    const text = readFileSync(join(root, f), 'utf8');
    const names = [
      ...[...text.matchAll(/Grant::\\?"([^"\\]*)\\?"/g)].map((m) => m[1]),
      ...[...text.matchAll(/"type":\s*"Grant",\s*"id":\s*"([^"]*)"/g)].map((m) => m[1]),
    ];
    names.forEach((g, i) => { if (!known(g)) error(`${f}: the grant name number ${i + 1} is not a grant the vocabulary declares`); });
  }
}
finish(checked, 'example installations');
