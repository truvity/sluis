// Shared helpers for the repository rule checks in tools/checks/. No dependency beyond Node.
//
// Every check takes an optional repository root as its first argument (the self-test points
// it at a scratch repository) and defaults to the root of this checkout.
import { execFileSync } from 'node:child_process';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const repoRoot = (argv = process.argv.slice(2)) =>
  resolve(argv.find((a) => !a.startsWith('--')) ?? join(dirname(fileURLToPath(import.meta.url)), '..', '..'));

// The files git tracks, as paths relative to the root.
export function trackedFiles(root) {
  const out = execFileSync('git', ['-C', root, 'ls-files', '-z'], { encoding: 'utf8', maxBuffer: 64 << 20 });
  return out.split('\0').filter(Boolean);
}

// One reporter per check: errors fail the run, an empty sweep fails the run.
export function reporter(name) {
  const errors = [];
  return {
    error: (m) => errors.push(m),
    errors,
    // `checked` is the number of things the check looked at; zero is a failure, not a pass.
    finish(checked, what) {
      for (const m of errors) console.error(`error: ${m}`);
      if (errors.length) {
        console.error(`${name}: ${errors.length} errors`);
        process.exit(1);
      }
      if (!checked) {
        console.error(`${name}: no ${what} found; an empty sweep proves nothing`);
        process.exit(1);
      }
      console.log(`${name}: ${checked} ${what}, no errors`);
      process.exit(0);
    },
  };
}
