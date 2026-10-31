#!/usr/bin/env node
// Mints a short-lived, read-only GitHub token from the sluis-status GitHub App and prints
// it to stdout, and nothing else. site/scripts/cf-build.sh runs it in Workers Builds, so the
// status page can read the project board there as it does in GitHub CI, where
// actions/create-github-app-token does the same (.github/workflows/site.yaml).
//
//   SLUIS_STATUS_APP_CLIENT_ID=... SLUIS_STATUS_APP_KEY=... \
//     site/scripts/mint-status-token.mjs --owner <org> --repo <repository>
//
// Environment:
//   SLUIS_STATUS_APP_CLIENT_ID  the App's client id (not a secret)
//   SLUIS_STATUS_APP_KEY        the App's private key: the PEM itself, or the PEM in base64.
//                               A dashboard may turn newlines into spaces or a literal \n;
//                               both are repaired.
// Arguments (or SLUIS_STATUS_APP_OWNER and SLUIS_STATUS_APP_REPO):
//   --owner  the organisation that installed the App
//   --repo   the one repository the token may reach
//
// The token gets the permissions the status build reads, as in site.yaml: issues: read and
// organization_projects: read (metadata: read is implied), on that one repository, for one
// hour at most. Nothing but the token is printed to stdout; an error names the step that
// failed and never the key, the signed JWT or the token. No dependency: node:crypto signs
// the JWT (RFC 7519) with RS256 (RFC 7518), as GitHub requires.
import { createPrivateKey, sign } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const API = 'https://api.github.com';
export const PERMISSIONS = Object.freeze({ issues: 'read', organization_projects: 'read' });

export class MintError extends Error {}

const b64url = (buf) => Buffer.from(buf).toString('base64url');

// The private key as a PEM, from a PEM or from base64 of a PEM, with the newlines repaired.
export function normalizeKey(raw) {
  let text = String(raw ?? '').trim();
  if (!text) throw new MintError('read the key: SLUIS_STATUS_APP_KEY is empty');
  if (!text.includes('-----BEGIN')) {
    text = Buffer.from(text.replace(/\s+/g, ''), 'base64').toString('utf8');
    if (!text.includes('-----BEGIN')) throw new MintError('read the key: it is neither a PEM nor the base64 of a PEM');
  }
  const m = /-----BEGIN ([A-Z0-9 ]+)-----([\s\S]*?)-----END \1-----/.exec(text);
  if (!m) throw new MintError('read the key: no complete PEM block');
  // Keep the base64 alphabet only: this drops real newlines, spaces and a literal \n alike.
  const body = m[2].replace(/\\n/g, '').replace(/[^A-Za-z0-9+/=]/g, '');
  if (!body) throw new MintError('read the key: the PEM block is empty');
  return `-----BEGIN ${m[1]}-----\n${body.match(/.{1,64}/g).join('\n')}\n-----END ${m[1]}-----\n`;
}

// An RS256 JWT for the App. GitHub allows at most 10 minutes and tolerates clock drift in one
// direction only, so iat is 60 seconds in the past and exp is 9 minutes ahead of now.
export function signJwt({ clientId, key, now = Math.floor(Date.now() / 1000) }) {
  if (!clientId) throw new MintError('read the client id: SLUIS_STATUS_APP_CLIENT_ID is empty');
  let privateKey;
  try {
    privateKey = createPrivateKey(normalizeKey(key));
  } catch (e) {
    if (e instanceof MintError) throw e;
    throw new MintError('read the key: it is not a valid private key');
  }
  if (privateKey.asymmetricKeyType !== 'rsa') throw new MintError('read the key: GitHub Apps sign with an RSA key');
  const head = b64url(JSON.stringify({ alg: 'RS256', typ: 'JWT' }));
  const claims = b64url(JSON.stringify({ iss: clientId, iat: now - 60, exp: now + 540 }));
  const signature = sign('RSA-SHA256', Buffer.from(`${head}.${claims}`), privateKey);
  return `${head}.${claims}.${b64url(signature)}`;
}

// One call to the API. A network error, a timeout or a 5xx is tried again (three attempts);
// a 4xx is an answer. The error carries the status and GitHub's `message`, never a body.
async function call(step, fetchImpl, method, path, bearer, body) {
  let last = '';
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      const res = await fetchImpl(`${API}${path}`, {
        method,
        headers: {
          Accept: 'application/vnd.github+json',
          Authorization: `Bearer ${bearer}`,
          'X-GitHub-Api-Version': '2022-11-28',
          'User-Agent': 'sluis-site-build',
          ...(body ? { 'Content-Type': 'application/json' } : {}),
        },
        body: body ? JSON.stringify(body) : undefined,
        signal: AbortSignal.timeout(20_000),
      });
      let json = null;
      try { json = await res.json(); } catch { /* not JSON */ }
      if (res.ok) return json;
      const message = typeof json?.message === 'string' ? `: ${json.message.slice(0, 200)}` : '';
      last = `HTTP ${res.status}${message}`;
      if (res.status < 500) break;
    } catch (e) {
      last = `no answer (${e?.name ?? 'error'})`;
    }
    if (attempt < 3) await new Promise((r) => setTimeout(r, 1000 * attempt));
  }
  throw new MintError(`${step}: ${last}`);
}

// The token. `fetchImpl` is injectable so the test needs no network.
export async function mintToken({ clientId, key, owner, repo, fetchImpl = fetch, now }) {
  const name = /^[A-Za-z0-9._-]+$/;
  if (!name.test(owner ?? '')) throw new MintError('read the arguments: --owner is missing or not a GitHub name');
  if (!name.test(repo ?? '')) throw new MintError('read the arguments: --repo is missing or not a GitHub name');
  const jwt = signJwt({ clientId, key, now });
  const found = await call('find the installation of the App on the organisation', fetchImpl, 'GET', `/orgs/${owner}/installation`, jwt);
  if (!Number.isSafeInteger(found?.id)) throw new MintError('find the installation of the App on the organisation: no installation id in the answer');
  const made = await call('create the installation token', fetchImpl, 'POST', `/app/installations/${found.id}/access_tokens`, jwt, {
    repositories: [repo],
    permissions: PERMISSIONS,
  });
  if (typeof made?.token !== 'string' || !made.token) throw new MintError('create the installation token: no token in the answer');
  return made.token;
}

function arg(name) {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 ? process.argv[i + 1] : undefined;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const token = await mintToken({
      clientId: process.env.SLUIS_STATUS_APP_CLIENT_ID,
      key: process.env.SLUIS_STATUS_APP_KEY,
      owner: arg('owner') ?? process.env.SLUIS_STATUS_APP_OWNER,
      repo: arg('repo') ?? process.env.SLUIS_STATUS_APP_REPO,
    });
    process.stdout.write(`${token}\n`);
  } catch (e) {
    console.error(`mint-status-token: failed to ${e instanceof MintError ? e.message : 'run'}`);
    process.exit(1);
  }
}
