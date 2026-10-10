import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { CloudflarePresetSchema, CloudflarePrototypeSchema, CloudflareStoredSchema } from "./gen/sluis/v1/cloudflare_pb";
import { awsProfile, credentialProcess, freshness, freshnessNote, prototypeView, span, tokenCommand } from "./cloudflareModel";

const now = new Date("2026-10-09T12:00:00Z");
const secondsAgo = (n: number) => ({ seconds: BigInt(Math.floor(now.getTime() / 1000) - n), nanos: 0 });
const preset = create(CloudflarePresetSchema, { name: "dns", rotationSeconds: 1200n, lifetimeSeconds: 3600n });
const stored = (age: number) => create(CloudflareStoredSchema, { present: true, mintedAt: secondsAgo(age) });

describe("the prototype's chip", () => {
  const view = (status: string, detail = "") => prototypeView(create(CloudflarePrototypeSchema, { id: "p", status, detail }));

  it("shows a disabled prototype as usable and everything else as not", () => {
    expect(view("ok")).toMatchObject({ kind: "ok", label: "disabled ✓", usable: true });
    for (const status of ["active", "forbidden", "missing", "unreachable", ""]) expect(view(status).usable).toBe(false);
  });

  it("says why a prototype is refused", () => {
    expect(view("active")).toMatchObject({ kind: "refused", label: "ACTIVE" });
    expect(view("forbidden", "Billing Read").title).toContain("Billing Read");
    expect(view("forbidden").label).toBe("forbidden permission");
  });

  it("does not claim a verdict for a prototype it could not read", () => {
    expect(prototypeView(undefined)).toMatchObject({ kind: "failed", label: "not read" });
  });
});

describe("how fresh the stored token is", () => {
  it("is fresh within one rotation, late past it, stale past two", () => {
    expect(freshness(preset, stored(600), now)).toBe("fresh");
    expect(freshness(preset, stored(1200), now)).toBe("fresh");
    expect(freshness(preset, stored(1201), now)).toBe("late");
    expect(freshness(preset, stored(2400), now)).toBe("late");
    expect(freshness(preset, stored(2401), now)).toBe("stale");
  });

  it("has nothing to judge before the first token", () => {
    expect(freshness(preset, create(CloudflareStoredSchema, {}), now)).toBe("none");
    expect(freshness(preset, undefined, now)).toBe("none");
  });

  it("does not call a token minted in the future stale", () => {
    expect(freshness(preset, stored(-60), now)).toBe("fresh");
  });

  it("explains the two that need explaining, and is silent when fresh", () => {
    expect(freshnessNote("stale", 1200n)).toContain("twice its rotation (20m)");
    expect(freshnessNote("fresh", 1200n)).toBe("");
  });
});

describe("durations", () => {
  it("uses the largest unit that is exact", () => {
    expect([90, 1200, 3600, 86400, 0].map(span)).toEqual(["90s", "20m", "1h", "1d", "—"]);
  });
});

describe("the snippets a person copies", () => {
  it("runs the R2 credentials through credential_process", () => {
    expect(credentialProcess("backup")).toBe("sluisctl cloudflare r2 backup");
    const profile = awsProfile("backup", "https://acct.r2.cloudflarestorage.com");
    expect(profile).toContain("credential_process = sluisctl cloudflare r2 backup");
    expect(profile).toContain("endpoint_url = https://acct.r2.cloudflarestorage.com");
    expect(profile).toContain("request_checksum_calculation = when_required");
    expect(profile).toContain("response_checksum_validation = when_required");
    expect(profile).toContain("addressing_style = path");
  });

  it("quotes a name that is not a plain word", () => {
    expect(tokenCommand("dns")).toBe("sluisctl cloudflare token dns --format env");
    expect(tokenCommand("a b; rm -rf")).toBe("sluisctl cloudflare token 'a b; rm -rf' --format env");
  });
});
