/** Words for a failed call, kept free of the Connect clients so they can
 *  be tested.
 *
 *  A refusal from the HUB with `unauthenticated` really does mean this
 *  browser's sign-in is gone. A refusal from the ISSUER does not: the
 *  page is plainly signed in, it is the issuer's own session listing
 *  that declined, and telling the operator to reload sends them round a
 *  loop that ends in the same sentence. */
const code = /^\[([a-z_]+)\]\s*/;

export function hubReason(err: unknown): string {
  if (!(err instanceof Error)) return String(err);
  if (/^\[unauthenticated\]/.test(err.message)) {
    return "Your sign-in here has ended — reload the page to sign in again.";
  }
  return err.message.replace(code, "");
}

export function issuerReason(err: unknown): string {
  if (!(err instanceof Error)) return String(err);
  const found = code.exec(err.message)?.[1];
  if (found === "unauthenticated" || found === "permission_denied") {
    return `The sign-in issuer refused the request for sessions. The console itself is still signed in; the issuer did not accept this browser's own session with it, and signing in again at the issuer usually clears that.`;
  }
  return err.message.replace(code, "");
}

/** What a list shows: its failure, or its emptiness, never both.
 *
 *  An empty state under an error says "nothing is there" about a read
 *  that never happened. */
export function listView(state: { loading: boolean; error?: string; count: number }): "loading" | "error" | "empty" | "rows" {
  if (state.error) return "error";
  if (state.loading && state.count === 0) return "loading";
  return state.count === 0 ? "empty" : "rows";
}
