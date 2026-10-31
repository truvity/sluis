#!/usr/bin/env node
// YAML files use the .yaml extension. A tracked *.yml file fails, except where a tool reads
// only that name:
//   - .github/ISSUE_TEMPLATE/config.yml: GitHub's issue template chooser looks for config.yml;
//   - .yarnrc.yml (any directory): Yarn's settings file has that name.
// The issue forms themselves (.github/ISSUE_TEMPLATE/*.yaml) and workflows accept .yaml.
//
// usage: tools/checks/yaml-ext.mjs [repo-root]
import { basename } from 'node:path';
import { reporter, repoRoot, trackedFiles } from './lib.mjs';

const EXCEPT_PATHS = new Set(['.github/ISSUE_TEMPLATE/config.yml']);
const EXCEPT_NAMES = new Set(['.yarnrc.yml']);

const r = reporter('check-yaml-ext');
let yaml = 0;
for (const path of trackedFiles(repoRoot())) {
  if (/\.yaml$/i.test(path)) { yaml++; continue; }
  if (!/\.yml$/i.test(path)) continue;
  yaml++;
  if (EXCEPT_PATHS.has(path) || EXCEPT_NAMES.has(basename(path))) continue;
  r.error(`${path}: use the .yaml extension`);
}
r.finish(yaml, 'tracked YAML files');
