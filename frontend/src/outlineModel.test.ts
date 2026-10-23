import { describe, expect, it } from "vitest";

import {
  attentionCount,
  breakerSentence,
  keyUserNotice,
  narrowed,
  outcomeView,
  parseReport,
  passNotice,
  permissionChange,
  roleChange,
  scopeLabel,
  stateView,
  tripped,
  when,
} from "./outlineModel";

// A document as the controller writes it: empty fields left out.
const document = {
  version: 1,
  workspace: "docs",
  enabled: false,
  url: "https://docs.example.com",
  key_user: "bot@example.com",
  tick: { at: "2026-10-24T10:00:00Z", outcome: "dry-run", changes: 3, held: 1 },
  users: [
    { email: "ann@example.com", id: "u1", role: "member", state: "ok" },
    { email: "bob@example.com", id: "u2", role: "member", target: "admin", state: "will-change", action: "promote" },
    { email: "bot@example.com", id: "u3", role: "admin", state: "ignored" },
  ],
  groups: [
    { name: "platform", id: "g1", state: "ok", members: [{ email: "ann@example.com", user_id: "u1", state: "ok" }] },
    { name: "eng", id: "g2", state: "ok", members: [{ email: "ann@example.com", user_id: "u1", state: "ok" }, { email: "cy@example.org", user_id: "u4", state: "held", reason: "no account" }] },
    { name: "new", state: "will-create" },
  ],
  collections: [
    { name: "Handbook", id: "c1", mode: "extend", state: "ok", grants: [{ group: "platform", group_id: "g1", permission: "read", target: "read", state: "ok" }] },
    {
      name: "Runbooks",
      id: "c2",
      mode: "strict",
      directory: "example.com",
      state: "ok",
      grants: [{ group: "eng", group_id: "g2", permission: "read", target: "read_write", state: "will-change" }],
      members: [{ email: "dee@example.org", user_id: "u5", permission: "read", state: "will-remove" }],
    },
  ],
  reported: [{ kind: "share", subject: "Onboarding", collection: "Handbook", detail: "shared publicly" }],
  shares_scanned_at: "0001-01-01T00:00:00Z",
  breakers: [
    { scope: "admins", affected: 2, total: 3, reason: "more than half the admins", fingerprint: "f1" },
    { scope: "group:eng", affected: 5, total: 6, reason: "most of the group", fingerprint: "f2", confirmed: true },
  ],
};

const parsed = () => {
  const { report, error } = parseReport(JSON.stringify(document));
  expect(error).toBeUndefined();
  return report!;
};

describe("reading an Outline report", () => {
  it("has nothing to read before the first pass", () => {
    expect(parseReport("")).toEqual({});
  });

  it("says when the document cannot be read, or is of another version", () => {
    expect(parseReport("{not json").error).toMatch(/could not be read/);
    expect(parseReport(JSON.stringify({ ...document, version: 2 })).error).toMatch(/version 2/);
  });

  it("fills in what the document leaves out", () => {
    const report = parsed();
    expect(report.held).toBe("");
    expect(report.tick).toEqual({ at: "2026-10-24T10:00:00Z", outcome: "dry-run", error: "", changes: 3, held: 1, retrying: 0 });
    expect(report.groups[2].members).toEqual([]);
    expect(report.collections[0].members).toEqual([]);
    expect(report.users[0]).toMatchObject({ target: "", suspended: false, reason: "" });
  });

  it("reads Go's zero time as no time", () => {
    expect(when(parsed().shares_scanned_at)).toBeUndefined();
    expect(when("")).toBeUndefined();
    expect(when(parsed().tick.at)?.toISOString()).toBe("2026-10-24T10:00:00.000Z");
  });
});

describe("what needs attention", () => {
  it("keeps only the rows not as the policy says, and the groups and collections that hold one", () => {
    const shown = narrowed(parsed(), false);
    expect(shown.users.map((u) => u.email)).toEqual(["bob@example.com", "bot@example.com"]);
    expect(shown.groups.map((g) => [g.name, g.members.map((m) => m.email)])).toEqual([
      ["eng", ["cy@example.org"]],
      ["new", []],
    ]);
    expect(shown.collections.map((c) => [c.name, c.grants.length, c.members.length])).toEqual([["Runbooks", 1, 1]]);
  });

  it("keeps everything when asked to", () => {
    const report = parsed();
    expect(narrowed(report, true)).toEqual({ users: report.users, groups: report.groups, collections: report.collections });
  });

  it("counts every row that needs attention", () => {
    // bob, bot; cy, the new group; the eng grant, dee.
    expect(attentionCount(parsed())).toBe(6);
  });
});

describe("breakers", () => {
  it("shows only the ones not yet confirmed", () => {
    expect(tripped(parsed()).map((b) => b.scope)).toEqual(["admins"]);
    expect(tripped(undefined)).toEqual([]);
  });

  it("names the scope in words", () => {
    expect(scopeLabel("admins")).toBe("admin roles");
    expect(scopeLabel("suspensions")).toBe("suspensions");
    expect(scopeLabel("group:eng")).toBe("group eng");
    expect(scopeLabel("collection:a:b")).toBe("collection a:b");
    expect(breakerSentence(parsed().breakers[0])).toBe("Held, admin roles: 2 of 3 at once.");
  });
});

describe("chips", () => {
  it("draws every row state the controller writes, and an unknown one as spelled", () => {
    for (const state of ["ok", "will-add", "will-remove", "will-change", "will-suspend", "will-activate", "will-create", "will-adopt", "held", "retrying", "reported", "ignored"]) {
      expect(stateView(state).kind).not.toBe("unrecognised");
    }
    expect(stateView("will-explode")).toMatchObject({ kind: "unrecognised", label: "will-explode" });
  });

  it("draws every outcome, and waiting as not connected", () => {
    for (const outcome of ["in-sync", "applied", "dry-run", "held", "retrying", "failed"]) {
      expect(outcomeView(outcome).kind).toBe(outcome);
    }
    expect(outcomeView("waiting").kind).toBe("not-connected");
    expect(outcomeView("later").kind).toBe("unrecognised");
  });
});

describe("words", () => {
  it("shows a role and what it becomes", () => {
    expect(roleChange("member", "")).toBe("member");
    expect(roleChange("member", "member")).toBe("member");
    expect(roleChange("member", "admin")).toBe("member → admin");
  });

  it("shows a grant and what it becomes, empty as none", () => {
    expect(permissionChange("read", "read")).toBe("read");
    expect(permissionChange("", "read")).toBe("none → read");
    expect(permissionChange("read_write", "")).toBe("read_write → none");
  });

  it("warns when the key's own account is not ignored", () => {
    expect(keyUserNotice("bot@example.com", true).severity).toBe("success");
    const warning = keyUserNotice("bot@example.com", false);
    expect(warning.severity).toBe("warning");
    expect(warning.text).toContain("bot@example.com");
    expect(warning.text).toContain("add this address to the workspace's ignore list in the policy");
  });

  it("says a second request within the minute was not kept", () => {
    expect(passNotice("docs", true)).toMatchObject({ severity: "success" });
    expect(passNotice("docs", false).text).toMatch(/^Asked; at most once a minute/);
  });
});
