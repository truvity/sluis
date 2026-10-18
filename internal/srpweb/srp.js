/*
 * SRP-6a for the sign-in page (docs/decisions/0076): RFC 2945's protocol with
 * RFC 5054's arithmetic, the 3072-bit group of RFC 5054 Appendix A, SHA-256.
 * The password never leaves the page: the server is sent a username, then
 * A and a proof M1, and answers with its own proof M2, which this script
 * checks before the page moves on.
 *
 * Self-hosted, no dependency, no inline script: the page's policy is
 * default-src 'self'. WebCrypto hashes; BigInt does the arithmetic (not
 * constant-time, which matters little for a value used once on the user's own
 * machine). srp/srp.go is the reference this is held to (internal/srpweb has
 * the cross test).
 */
(function (root) {
  'use strict';

  var N = BigInt('0x' +
    'ffffffffffffffffc90fdaa22168c234c4c6628b80dc1cd129024e088a67cc74' +
    '020bbea63b139b22514a08798e3404ddef9519b3cd3a431b302b0a6df25f1437' +
    '4fe1356d6d51c245e485b576625e7ec6f44c42e9a637ed6b0bff5cb6f406b7ed' +
    'ee386bfb5a899fa5ae9f24117c4b1fe649286651ece45b3dc2007cb8a163bf05' +
    '98da48361c55d39a69163fa8fd24cf5f83655d23dca3ad961c62f356208552bb' +
    '9ed529077096966d670c354e4abc9804f1746c08ca18217c32905e462e36ce3b' +
    'e39e772c180e86039b2783a2ec07a28fb5c55df06f4c52c9de2bcbf695581718' +
    '3995497cea956ae515d2261898fa051015728e5a8aaac42dad33170d04507a33' +
    'a85521abdf1cba64ecfb850458dbef0a8aea71575d060c7db3970f85a6e1e4c7' +
    'abf5ae8cdb0933d71e8c94e04a25619dcee3d2261ad2ee6bf12ffa06d98a0864' +
    'd87602733ec86a64521f2b18177b200cbbe117577a615d6c770988c0bad946e2' +
    '08e24fa074e5ab3143db5bfce0fd108e4b82d120a93ad2caffffffffffffffff');
  var G = 5n;
  var SIZE = 384; // bytes in a group element

  var utf8 = new TextEncoder();

  function hex(bytes) {
    var out = '';
    for (var i = 0; i < bytes.length; i++) {
      out += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
    }
    return out;
  }

  function unhex(text) {
    if (typeof text !== 'string' || text.length % 2 !== 0 || !/^[0-9a-f]*$/i.test(text)) {
      throw new Error('malformed value');
    }
    var out = new Uint8Array(text.length / 2);
    for (var i = 0; i < out.length; i++) {
      out[i] = parseInt(text.substr(i * 2, 2), 16);
    }
    return out;
  }

  function toBig(bytes) { return BigInt('0x' + (hex(bytes) || '0')); }

  // PAD(): the fixed-width big-endian form of a group element.
  function pad(x) {
    var text = x.toString(16);
    if (text.length > SIZE * 2) { throw new Error('value too large'); }
    return unhex(text.padStart(SIZE * 2, '0'));
  }

  function concat(parts) {
    var length = 0;
    parts.forEach(function (p) { length += p.length; });
    var out = new Uint8Array(length), at = 0;
    parts.forEach(function (p) { out.set(p, at); at += p.length; });
    return out;
  }

  async function H() {
    var parts = Array.prototype.slice.call(arguments);
    return new Uint8Array(await root.crypto.subtle.digest('SHA-256', concat(parts)));
  }

  function mod(a, m) { return ((a % m) + m) % m; }

  // base^exp mod m, square and multiply.
  function modPow(base, exp, m) {
    var result = 1n;
    base = mod(base, m);
    while (exp > 0n) {
      if (exp & 1n) { result = (result * base) % m; }
      exp >>= 1n;
      if (exp > 0n) { base = (base * base) % m; }
    }
    return result;
  }

  var MIN_ROUNDS = 100000, MAX_ROUNDS = 10000000;

  // PBKDF2-HMAC-SHA256 of the inner hash, for the number of rounds the server
  // named: x is stretched so a stolen verifier costs that many hashes a guess.
  async function stretch(inner, salt, rounds) {
    var key = await root.crypto.subtle.importKey('raw', inner, 'PBKDF2', false, ['deriveBits']);
    return new Uint8Array(await root.crypto.subtle.deriveBits(
      { name: 'PBKDF2', hash: 'SHA-256', salt: salt, iterations: rounds }, key, 256));
  }

  // A client for one run. Nothing here is kept after the run. The username is not
  // known yet: the server spells it (lower case, trimmed) and the proof must use
  // its spelling, so it comes with the salt (respond).
  async function client(password, random) {
    var rnd = new Uint8Array(32);
    (random || root.crypto).getRandomValues(rnd);
    var a = toBig(rnd);
    if (a === 0n) { throw new Error('no randomness'); }
    var A = modPow(G, a, N);
    var k = toBig(await H(pad(N), pad(G)));
    var state = {};

    return {
      A: hex(pad(A)),
      // respond takes the username as the server spells it, the salt and B as
      // hexadecimal and the number of rounds, and returns M1 as hexadecimal.
      respond: async function (username, saltHex, bHex, rounds) {
        if (typeof username !== 'string' || username === '') { throw new Error('no username'); }
        if (!Number.isInteger(rounds) || rounds < MIN_ROUNDS || rounds > MAX_ROUNDS) {
          throw new Error('unacceptable rounds');
        }
        var salt = unhex(saltHex);
        var bytes = unhex(bHex);
        if (bytes.length > SIZE) { throw new Error('invalid public value'); }
        var B = toBig(bytes);
        if (B >= N || mod(B, N) === 0n) { throw new Error('invalid public value'); }
        var u = toBig(await H(pad(A), pad(B)));
        if (u === 0n) { throw new Error('invalid public value'); }
        var inner = await H(utf8.encode(username), utf8.encode(':'), utf8.encode(password));
        var x = toBig(await H(salt, await stretch(inner, salt, rounds)));
        var base = mod(B - k * modPow(G, x, N), N);
        var S = modPow(base, a + u * x, N);
        var key = await H(pad(S));
        var hn = await H(pad(N)), hg = await H(pad(G));
        for (var i = 0; i < hn.length; i++) { hn[i] ^= hg[i]; }
        var m1 = await H(hn, await H(utf8.encode(username)), salt, pad(A), pad(B), key);
        state.m1 = m1;
        state.key = key;
        return hex(m1);
      },
      // verify checks the server's proof M2, given as hexadecimal.
      verify: async function (m2Hex) {
        if (!state.m1) { throw new Error('no proof was made'); }
        var want = await H(pad(A), state.m1, state.key);
        var got = unhex(m2Hex);
        var diff = want.length ^ got.length;
        for (var i = 0; i < want.length && i < got.length; i++) { diff |= want[i] ^ got[i]; }
        if (diff !== 0) { throw new Error('the server did not prove it holds the verifier'); }
      },
    };
  }

  function failure(response, body) {
    var err = new Error((body && body.error) || 'not accepted');
    err.status = response.status;
    return err;
  }

  async function post(url, payload) {
    var response = await root.fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    var body = null;
    try { body = await response.json(); } catch (e) { body = null; }
    if (!response.ok) { throw failure(response, body); }
    return body;
  }

  // signIn runs both requests and returns where to go next. opts: username,
  // password, state, start and finish (URLs), and optionally post.
  async function signIn(opts) {
    var send = opts.post || post;
    var c = await client(opts.password);
    var begun = await send(opts.start, { state: opts.state, username: opts.username });
    var m1 = await c.respond(begun.username, begun.salt, begun.B, begun.iterations);
    var done = await send(opts.finish, { state: opts.state, handle: begun.handle, A: c.A, M1: m1 });
    await c.verify(done.M2);
    return done.next;
  }

  // attach wires a form (data-srp) to signIn. The inputs carry no name, so a
  // browser that does not run this submits nothing.
  function attach(form) {
    var note = form.querySelector('[data-srp-error]');
    var button = form.querySelector('button[type=submit]');
    var user = form.querySelector('[data-srp-user]');
    var password = form.querySelector('[data-srp-password]');
    function say(text) {
      if (!note) { return; }
      note.textContent = text;
      note.hidden = !text;
    }
    form.addEventListener('submit', async function (event) {
      event.preventDefault();
      say('');
      button.disabled = true;
      try {
        var next = await signIn({
          username: user.value,
          password: password.value,
          state: form.dataset.state,
          start: form.dataset.start,
          finish: form.dataset.finish,
        });
        password.value = '';
        root.location.assign(next);
      } catch (err) {
        password.value = '';
        // The server's sentence when it gave one, which says nothing about why.
        say(err && err.status ? err.message : 'That sign-in did not complete.');
        button.disabled = false;
      }
    });
  }

  var api = { client: client, signIn: signIn, attach: attach };
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else {
    root.SluisSRP = api;
    if (root.document) {
      root.document.querySelectorAll('form[data-srp]').forEach(attach);
    }
  }
})(typeof globalThis !== 'undefined' ? globalThis : this);
