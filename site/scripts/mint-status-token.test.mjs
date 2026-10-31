// Tests for mint-status-token.mjs: the key formats, the JWT, and the two API calls against a
// fake fetch. No network. The key is made here at run time, so no key is committed.
// usage: node --test site/scripts/mint-status-token.test.mjs   (just site-test)
import assert from 'node:assert/strict';
import { generateKeyPairSync, verify } from 'node:crypto';
import test from 'node:test';
import { MintError, PERMISSIONS, mintToken, normalizeKey, signJwt } from './mint-status-token.mjs';

const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
const pkcs8 = privateKey.export({ type: 'pkcs8', format: 'pem' });
const pkcs1 = privateKey.export({ type: 'pkcs1', format: 'pem' });
const now = 1_800_000_000;
const decode = (part) => JSON.parse(Buffer.from(part, 'base64url').toString('utf8'));

function assertValidJwt(jwt, clientId) {
  const [head, claims, sig] = jwt.split('.');
  assert.deepEqual(decode(head), { alg: 'RS256', typ: 'JWT' });
  assert.deepEqual(decode(claims), { iss: clientId, iat: now - 60, exp: now + 540 });
  assert.ok(decode(claims).exp - decode(claims).iat <= 600, 'a JWT lives 10 minutes at most');
  assert.ok(verify('RSA-SHA256', Buffer.from(`${head}.${claims}`), publicKey, Buffer.from(sig, 'base64url')), 'signature verifies');
}

test('the JWT is RS256 with iss, a backdated iat and an exp within 10 minutes', () => {
  assertValidJwt(signJwt({ clientId: 'Iv1.example', key: pkcs8, now }), 'Iv1.example');
});

test('every key form gives the same signature', () => {
  const forms = {
    'PKCS#8 PEM': pkcs8,
    'PKCS#1 PEM': pkcs1,
    'base64 of a PEM': Buffer.from(pkcs8).toString('base64'),
    'base64 of a PEM, wrapped': Buffer.from(pkcs8).toString('base64').replace(/(.{76})/g, '$1\n'),
    'newlines as spaces': pkcs8.trim().replace(/\n/g, ' '),
    'newlines as a literal backslash n': pkcs8.trim().replace(/\n/g, '\\n'),
    'surrounding blanks and CRLF': `\n  ${pkcs8.replace(/\n/g, '\r\n')}  \n`,
  };
  const want = signJwt({ clientId: 'c', key: pkcs8, now });
  for (const [name, key] of Object.entries(forms)) assert.equal(signJwt({ clientId: 'c', key, now }), want, name);
});

test('a bad key or client id is refused, and the message holds neither', () => {
  const secret = 'not-a-key-but-secret-looking-0123456789';
  for (const key of ['', '   ', secret, Buffer.from(secret).toString('base64')]) {
    assert.throws(() => signJwt({ clientId: 'c', key, now }), (e) => e instanceof MintError && !e.message.includes(secret));
  }
  assert.throws(() => normalizeKey(pkcs8.replace(/\n.*\n/, '\n!!!\n').slice(0, 60)), MintError);
  assert.throws(() => signJwt({ clientId: '', key: pkcs8, now }), /client id/);
  const ec = generateKeyPairSync('ec', { namedCurve: 'P-256' }).privateKey.export({ type: 'pkcs8', format: 'pem' });
  assert.throws(() => signJwt({ clientId: 'c', key: ec, now }), /RSA/);
});

// A fake GitHub: records the requests, answers from `routes` ("METHOD path" -> [status, body]).
function fakeGithub(routes) {
  const calls = [];
  const fetchImpl = async (url, init) => {
    const path = new URL(url).pathname;
    calls.push({ method: init.method, path, headers: init.headers, body: init.body ? JSON.parse(init.body) : undefined });
    const [status, body] = routes[`${init.method} ${path}`] ?? [404, { message: 'Not Found' }];
    return new Response(JSON.stringify(body), { status });
  };
  return { calls, fetchImpl };
}
const args = { clientId: 'Iv1.example', key: pkcs8, owner: 'acme', repo: 'widgets', now };

test('mintToken finds the installation, then asks for the two read permissions on one repository', async () => {
  const token = 'tok-example-0001';
  const { calls, fetchImpl } = fakeGithub({
    'GET /orgs/acme/installation': [200, { id: 4242 }],
    'POST /app/installations/4242/access_tokens': [201, { token }],
  });
  assert.equal(await mintToken({ ...args, fetchImpl }), token);
  assert.deepEqual(calls.map((c) => `${c.method} ${c.path}`), ['GET /orgs/acme/installation', 'POST /app/installations/4242/access_tokens']);
  for (const c of calls) assertValidJwt(c.headers.Authorization.replace('Bearer ', ''), 'Iv1.example');
  assert.deepEqual(calls[1].body, { repositories: ['widgets'], permissions: { issues: 'read', organization_projects: 'read' } });
  assert.deepEqual(PERMISSIONS, calls[1].body.permissions);
});

test('a failing step is named, and no secret is in the error', async () => {
  const jwtPrefix = signJwt({ clientId: 'Iv1.example', key: pkcs8, now }).split('.')[0];
  const cases = [
    [{ 'GET /orgs/acme/installation': [404, { message: 'Not Found' }] }, /find the installation.*HTTP 404: Not Found/],
    [{ 'GET /orgs/acme/installation': [401, { message: 'A JSON web token could not be decoded' }] }, /find the installation.*HTTP 401/],
    [{ 'GET /orgs/acme/installation': [200, {}] }, /find the installation.*no installation id/],
    [{ 'GET /orgs/acme/installation': [200, { id: 7 }], 'POST /app/installations/7/access_tokens': [422, { message: 'Validation Failed' }] }, /create the installation token.*HTTP 422/],
    [{ 'GET /orgs/acme/installation': [200, { id: 7 }], 'POST /app/installations/7/access_tokens': [201, {}] }, /create the installation token.*no token/],
  ];
  for (const [routes, want] of cases) {
    const { fetchImpl } = fakeGithub(routes);
    await assert.rejects(mintToken({ ...args, fetchImpl }), (e) => {
      assert.match(e.message, want);
      assert.ok(!e.message.includes(jwtPrefix) && !e.message.includes('BEGIN'), 'no JWT or key in the message');
      return e instanceof MintError;
    });
  }
});

test('a name that is not a GitHub name never reaches the API', async () => {
  const { calls, fetchImpl } = fakeGithub({});
  await assert.rejects(mintToken({ ...args, owner: '../x', fetchImpl }), /--owner/);
  await assert.rejects(mintToken({ ...args, repo: undefined, fetchImpl }), /--repo/);
  assert.equal(calls.length, 0);
});
