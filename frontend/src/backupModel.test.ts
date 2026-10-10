import { describe, expect, it } from "vitest";

import { counts, freshness, freshnessNote, maintenanceLine, newestFirst, progress, restoreHowTo, retentionText, runState } from "./backupModel";

const now = new Date("2026-10-10T12:00:00Z");
const hoursAgo = (h: number) => ({ seconds: BigInt(Math.floor(now.getTime() / 1000) - h * 3600), nanos: 0 });

describe("a run's state", () => {
  it("has a tone for each state the module writes", () => {
    expect(runState("completed").tone).toBe("success");
    expect(runState("running").tone).toBe("info");
    expect(runState("paused").tone).toBe("warning");
    expect(runState("failed").tone).toBe("error");
  });

  it("shows a word it does not know as it is, and an empty one as unknown", () => {
    expect(runState("verifying")).toEqual({ label: "verifying", tone: "default" });
    expect(runState("").label).toBe("unknown");
  });
});

describe("progress of an unfinished run", () => {
  it("is units done of units", () => {
    expect(progress({ units: 8, done: 3 })).toBe("3 of 8 units (37%)");
  });

  it("says nothing without a total, and never passes the total", () => {
    expect(progress({ units: 0, done: 0 })).toBeUndefined();
    expect(progress(undefined)).toBeUndefined();
    expect(progress({ units: 4, done: 9 })).toBe("4 of 4 units (100%)");
  });
});

describe("the last success against the schedule", () => {
  it("is none before any backup completed", () => {
    expect(freshness(undefined, now)).toBe("none");
    expect(freshnessNote("none")).toContain("No backup has completed");
  });

  it("is stale past 36 hours and fresh before", () => {
    expect(freshness({ finished: hoursAgo(35) }, now)).toBe("fresh");
    expect(freshness({ finished: hoursAgo(37) }, now)).toBe("stale");
    expect(freshnessNote("stale")).toContain("36 hours");
    expect(freshnessNote("fresh")).toBe("");
  });

  it("falls back to the start when a run has no finish", () => {
    expect(freshness({ started: hoursAgo(40) }, now)).toBe("stale");
  });
});

describe("the list of backups", () => {
  it("is newest first and keeps a backup whose manifest could not be read", () => {
    const list = [
      { id: "a", created: hoursAgo(48) },
      { id: "b", created: hoursAgo(24) },
      { id: "c", created: undefined },
    ];
    expect(newestFirst(list).map((b) => b.id)).toEqual(["b", "a", "c"]);
  });

  it("describes counts, and an unreadable manifest as that", () => {
    expect(counts({ modules: 4, records: 90n, chunks: 6 })).toBe("4 modules · 90 records · 6 chunks");
    expect(counts({ modules: 0, records: 0n, chunks: 0, error: "bad" })).toBe("manifest unreadable");
  });
});

describe("retention", () => {
  it("says what the last pass did", () => {
    expect(retentionText({ keep: 7, maxAge: "720h", kept: 7, removed: 2, cleaned: 0, refused: 1 })).toBe(
      "The newest 7 always stay, and another goes when older than 720h. Last pass: kept 7, removed 2, 1 deletes refused.",
    );
  });

  it("says when none has run", () => {
    expect(retentionText(undefined)).toContain("No retention pass");
  });
});

describe("modules under maintenance", () => {
  it("names the module as a person does and who set the flag", () => {
    expect(maintenanceLine({ module: "oidc", state: "restoring", by: "ada", unreadable: false })).toBe("sign-in: restoring, set by ada");
  });

  it("treats a flag it could not read as set", () => {
    expect(maintenanceLine({ module: "slack", state: "", by: "", unreadable: true })).toContain("treated as set");
  });
});

describe("starting a restore", () => {
  it("points at sluisctl and offers nothing in the console", () => {
    expect(restoreHowTo).toContain("sluisctl");
    expect(restoreHowTo).toContain("not from this console");
  });
});
