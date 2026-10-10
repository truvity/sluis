// The console calls the hub and the issuer under the proto package
// sluis.v1. A server older than that serves the same services under the
// legacy packages (directoryroster.v1 for the hub, accessissuer.v1 for the
// issuer's SessionService), with messages that are identical on the wire,
// so a new console in front of an old server only needs the path changed.
//
// This wraps `fetch`: a call goes to the new path first, and is repeated
// at the old one when the server says the new path does not exist. Which
// answers say that matters, because Connect reports "not found" and
// "unimplemented" both as HTTP 404 and a blind retry would run a call
// twice that had in fact been handled:
//
//   - a Connect error body with code "unimplemented": the service is not
//     there, so the call never ran;
//   - a 404 that is not a Connect error at all (a router's own page): the
//     path is not routed, so the call never ran.
//
// A Connect error with any other code is the server's real answer and is
// returned as it is. Once a service has fallen back it keeps going to the
// old path for the life of the page rather than costing a request per
// call; a reload meets an upgraded server.
//
// Removed with the legacy packages.

const current = /\/sluis\.v1\.([A-Za-z]+)\/([A-Za-z]+)(?=[?#]|$)/;

function legacyPackage(service: string): string {
  return service === "SessionService" ? "accessissuer.v1" : "directoryroster.v1";
}

async function isMissing(response: Response): Promise<boolean> {
  if (response.status !== 404) return false;
  try {
    const body: unknown = JSON.parse(await response.clone().text());
    return typeof body === "object" && body !== null && "code" in body ? (body as { code: unknown }).code === "unimplemented" : true;
  } catch {
    return true;
  }
}

export function withLegacyFallback(inner: typeof fetch): typeof fetch {
  const fellBack = new Set<string>();
  return async (input, init) => {
    const target = typeof input === "string" ? input : input instanceof URL ? input.href : undefined;
    const match = target === undefined ? null : current.exec(target);
    if (target === undefined || match === null) return inner(input, init);

    const [, service] = match;
    const old = target.replace(current, `/${legacyPackage(service)}.${service}/${match[2]}`);
    if (fellBack.has(service)) return inner(old, init);

    const first = await inner(input, init);
    if (!(await isMissing(first))) return first;

    fellBack.add(service);
    return inner(old, init);
  };
}
