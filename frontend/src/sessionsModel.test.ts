import { describe, expect, it } from "vitest";

import { clientLabel, groupSessions, matchesClient, neverUsed, resourceLabel, type SessionFacts } from "./sessionsModel";

const NOW = Date.UTC(2026, 9, 10, 12, 0, 0);
const stamp = (msAgo: number) => ({ seconds: BigInt(Math.floor((NOW - msAgo) / 1000)), nanos: 0 });
const MIN = 60_000;

const session = (over: Partial<SessionFacts> & { id: string }): SessionFacts => ({
    identity: "ada@north.example",
    clientId: "https://app.example/oauth/metadata",
    clientName: "",
    resource: "",
    resourceName: "",
    scopes: [],
    issuedAt: stamp(60 * MIN),
    ...over,
});

describe("client label", () => {
    it("prefers the name the issuer resolved", () => {
        expect(clientLabel({ clientId: "https://app.example/m", clientName: "Example Assistant" })).toBe("Example Assistant");
    });
    it("falls back to the host of a URL id, then to the id", () => {
        expect(clientLabel({ clientId: "https://app.example/oauth/m", clientName: "" })).toBe("app.example");
        expect(clientLabel({ clientId: "argocd", clientName: "" })).toBe("argocd");
    });
});

describe("resource label", () => {
    it("uses the policy name, then the host, and says unknown for none", () => {
        expect(resourceLabel({ resource: "https://mcp.example/v1", resourceName: "Example MCP" })).toBe("Example MCP");
        expect(resourceLabel({ resource: "https://mcp.example/v1", resourceName: "" })).toBe("mcp.example");
        expect(resourceLabel({ resource: "", resourceName: "" })).toBe("unknown");
    });
});

describe("never used", () => {
    it("flags an old session that was never refreshed", () => {
        expect(neverUsed({ issuedAt: stamp(10 * MIN) }, NOW)).toBe(true);
    });
    it("leaves a fresh one alone: the client has not had time", () => {
        expect(neverUsed({ issuedAt: stamp(2 * MIN) }, NOW)).toBe(false);
    });
    it("leaves a used one alone however old", () => {
        expect(neverUsed({ issuedAt: stamp(600 * MIN), lastRefreshed: stamp(MIN) }, NOW)).toBe(false);
    });
    it("does not guess when the open time is missing", () => {
        expect(neverUsed({}, NOW)).toBe(false);
    });
});

describe("grouping", () => {
    const rows = [
        session({ id: "1", resource: "https://mcp.example/v1", lastRefreshed: stamp(5 * MIN) }),
        session({ id: "2", resource: "https://mcp.example/v1" }),
        session({ id: "3", resource: "https://mcp.example/v1", lastRefreshed: stamp(MIN) }),
        session({ id: "4", resource: "https://other.example/v1" }),
        session({ id: "5", identity: "eli@south.example", resource: "https://mcp.example/v1" }),
    ];
    const people = groupSessions(rows, NOW);

    it("groups by person, then client and resource", () => {
        expect(people.map((p) => p.identity)).toEqual(["ada@north.example", "eli@south.example"]);
        expect(people[0].total).toBe(4);
        expect(people[0].groups.map((g) => [g.resource, g.sessions.length])).toEqual([
            ["https://mcp.example/v1", 3],
            ["https://other.example/v1", 1],
        ]);
    });
    it("counts the unused ones and finds the latest use per group", () => {
        const [mcp, other] = people[0].groups;
        expect(mcp.unused).toBe(1);
        expect(mcp.lastUsed).toBe(NOW - MIN);
        expect(other.unused).toBe(1);
        expect(other.lastUsed).toBeUndefined();
    });
    it("keeps another person's identical sessions apart", () => {
        expect(people[1].groups).toHaveLength(1);
        expect(people[1].groups[0].sessions.map((s) => s.id)).toEqual(["5"]);
    });
});

describe("client filter", () => {
    const s = session({ id: "1", clientName: "Example Assistant", resource: "https://mcp.example/v1", resourceName: "Example MCP" });
    it("matches the name, the id and the resource", () => {
        for (const needle of ["assistant", "oauth/metadata", "example mcp", "mcp.example"]) {
            expect(matchesClient(s, needle)).toBe(true);
        }
        expect(matchesClient(s, "argo")).toBe(false);
    });
});
