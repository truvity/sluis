import { describe, expect, it } from "vitest";
import { withLegacyFallback } from "./legacyFallback";

function server(routes: Record<string, () => Response>) {
  const seen: string[] = [];
  const fetcher = (async (input: RequestInfo | URL) => {
    const url = String(input);
    seen.push(url);
    const route = routes[new URL(url).pathname];
    return route ? route() : new Response("404 page not found", { status: 404 });
  }) as typeof fetch;
  return { seen, fetcher };
}

const ok = () => new Response("{}", { status: 200 });
const connectError = (code: string) => () =>
  new Response(JSON.stringify({ code, message: "m" }), { status: 404, headers: { "content-type": "application/json" } });

describe("withLegacyFallback", () => {
  it("uses the new path when the server has it", async () => {
    const s = server({ "/c/sluis.v1.AccessService/ListHolders": ok });
    const res = await withLegacyFallback(s.fetcher)("http://h/c/sluis.v1.AccessService/ListHolders", { method: "POST" });
    expect(res.status).toBe(200);
    expect(s.seen).toEqual(["http://h/c/sluis.v1.AccessService/ListHolders"]);
  });

  it("falls back to directoryroster.v1 when the service is unimplemented, then goes there directly", async () => {
    const s = server({
      "/c/sluis.v1.AccessService/ListHolders": connectError("unimplemented"),
      "/c/directoryroster.v1.AccessService/ListHolders": ok,
    });
    const fetcher = withLegacyFallback(s.fetcher);
    expect((await fetcher("http://h/c/sluis.v1.AccessService/ListHolders", { method: "POST" })).status).toBe(200);
    expect((await fetcher("http://h/c/sluis.v1.AccessService/ListHolders", { method: "POST" })).status).toBe(200);
    expect(s.seen).toEqual([
      "http://h/c/sluis.v1.AccessService/ListHolders",
      "http://h/c/directoryroster.v1.AccessService/ListHolders",
      "http://h/c/directoryroster.v1.AccessService/ListHolders",
    ]);
  });

  it("falls back on a plain 404 that is not a Connect error", async () => {
    const s = server({ "/directoryroster.v1.GitHubService/GetGitHubStatus": ok });
    const res = await withLegacyFallback(s.fetcher)("http://h/sluis.v1.GitHubService/GetGitHubStatus", { method: "POST" });
    expect(res.status).toBe(200);
  });

  it("sends the issuer's SessionService to accessissuer.v1", async () => {
    const s = server({ "/accessissuer.v1.SessionService/ListSessions": ok });
    const res = await withLegacyFallback(s.fetcher)("http://h/sluis.v1.SessionService/ListSessions", { method: "POST" });
    expect(res.status).toBe(200);
  });

  it("does not repeat a call the server handled and answered not_found", async () => {
    const s = server({ "/sluis.v1.SlackService/Remove": connectError("not_found") });
    const res = await withLegacyFallback(s.fetcher)("http://h/sluis.v1.SlackService/Remove", { method: "POST" });
    expect(res.status).toBe(404);
    expect(s.seen).toHaveLength(1);
  });

  it("leaves a path that is not under sluis.v1 alone", async () => {
    const s = server({ "/audit/audit.v1.QueryService/Query": ok });
    await withLegacyFallback(s.fetcher)("http://h/audit/audit.v1.QueryService/Query", { method: "POST" });
    expect(s.seen).toHaveLength(1);
  });
});
