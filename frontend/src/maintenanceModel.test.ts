import { describe, expect, it } from "vitest";
import { maintenanceText } from "./maintenanceModel";

describe("maintenanceText", () => {
  it("says nothing when nothing is under maintenance", () => {
    expect(maintenanceText(undefined)).toBeUndefined();
    expect(maintenanceText({ module: "github", state: "" })).toBeUndefined();
  });

  it("names the module and says the pages are read-only", () => {
    const text = maintenanceText({ module: "github", state: "restoring", since: "2026-10-10T09:00:00Z", reason: "Restoring backup 7." });
    expect(text).toContain("GitHub is under maintenance since 2026-10-10 09:00:00 UTC");
    expect(text).toContain("read-only");
    expect(text).toContain("Restoring backup 7.");
  });

  it("falls back to the module's own name", () => {
    expect(maintenanceText({ module: "other", state: "restoring" })).toContain("Other is under maintenance");
  });
});
