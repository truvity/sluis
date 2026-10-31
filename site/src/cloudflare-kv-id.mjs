// Puts the id of the status KV namespace into the Worker configuration the build
// generates, from the environment variable SLUIS_STATUS_KV_ID, so that the id is never
// in this repository.
//
// wrangler.jsonc names the binding (STATUS) with a placeholder id, because Wrangler
// does not read environment variables in its configuration. The Cloudflare adapter
// writes the configuration to deploy to <output>/wrangler.json (and points
// .wrangler/deploy/config.json at it), and `wrangler deploy` uses that file. This
// integration rewrites the id in that generated file after the adapter has written it.
//
//   - When SLUIS_STATUS_KV_ID is set, the id goes into the generated configuration. A
//     value that is not a namespace id fails the build. The deploy job of site.yaml
//     sets it from an environment secret.
//   - When it is not set (a laptop, a pull request), the placeholder stays and the
//     build says so: that build deploys nothing.
//   - GitHub CI sets a fake id to exercise this path and `wrangler deploy --dry-run`.
//
// The repository holds no id, and the value is never printed or logged.
import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

export const BINDING = 'STATUS';
const VALID = /^[0-9a-f]{32}$/;

export default function cloudflareKvId() {
  return {
    name: 'sluis:cloudflare-kv-id',
    hooks: {
      'astro:build:done': async ({ dir, logger }) => {
        const id = process.env.SLUIS_STATUS_KV_ID?.trim();
        if (!id) {
          logger.info(`SLUIS_STATUS_KV_ID is not set: the ${BINDING} binding keeps its placeholder id, so this build cannot be deployed.`);
          return;
        }
        if (!VALID.test(id)) {
          throw new Error('SLUIS_STATUS_KV_ID is not a KV namespace id (32 lowercase hexadecimal characters).');
        }
        const file = fileURLToPath(new URL('./wrangler.json', dir));
        const config = JSON.parse(await readFile(file, 'utf8'));
        const kv = (config.kv_namespaces ?? []).filter((n) => n.binding === BINDING);
        if (kv.length !== 1) {
          throw new Error(`${file} declares ${kv.length} KV namespaces named ${BINDING}; wrangler.jsonc must declare exactly one.`);
        }
        kv[0].id = id;
        await writeFile(file, JSON.stringify(config));
        logger.info(`${BINDING} KV id taken from SLUIS_STATUS_KV_ID.`);
      },
    },
  };
}
