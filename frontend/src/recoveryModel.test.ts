import { describe, expect, it } from "vitest";
import {
  disabledNotice,
  recoveryHeadline,
  showDisable,
  supersededNotice,
  whenText,
  withheldReason,
  type Recovery,
} from "./recoveryModel";

const on: Recovery = {
  configured: true,
  effective: true,
  kind: "password",
  disabledBy: "",
  supersededBy: "",
  generation: "2026-10",
  durable: true,
  canDisable: true,
  cannotDisableReason: "",
};

const off: Recovery = { ...on, effective: false, canDisable: false, disabledAt: new Date(Date.UTC(2026, 9, 18, 20, 5)), disabledBy: "ada@example.com" };

describe("showDisable", () => {
  it("is shown to an operator who signed in through the identity provider while recovery is on", () => {
    expect(showDisable(on, true, "oidc")).toBe(true);
    expect(showDisable(on, true, "forwarded")).toBe(true);
  });

  it("is hidden from a recovery sign-in, even an operator by construction", () => {
    expect(showDisable(on, true, "recovery")).toBe(false);
  });

  it("is hidden from anyone who is not an operator", () => {
    expect(showDisable(on, false, "oidc")).toBe(false);
  });

  it("is hidden when recovery is already off, not enabled, or the server withholds it", () => {
    expect(showDisable(off, true, "oidc")).toBe(false);
    expect(showDisable({ ...on, configured: false, effective: false }, true, "oidc")).toBe(false);
    expect(showDisable({ ...on, canDisable: false }, true, "oidc")).toBe(false);
    expect(showDisable(undefined, true, "oidc")).toBe(false);
  });
});

describe("the words", () => {
  it("formats the time in UTC", () => {
    expect(whenText(new Date(Date.UTC(2026, 0, 2, 3, 4)))).toBe("2026-01-02 03:04 UTC");
  });

  it("says who turned it off and that only the configuration turns it back on", () => {
    expect(disabledNotice(new Date(Date.UTC(2026, 9, 18, 20, 5)), "ada@example.com")).toBe(
      "Recovery disabled at 2026-10-18 20:05 UTC by ada@example.com; re-enable only via configuration.",
    );
    expect(recoveryHeadline(off)).toContain("by ada@example.com");
  });

  it("describes both shapes while on, and nothing where recovery is not enabled", () => {
    expect(recoveryHeadline(on)).toContain("password");
    expect(recoveryHeadline({ ...on, kind: "token" })).toContain("cluster");
    expect(recoveryHeadline({ ...on, configured: false, effective: false })).toBeUndefined();
    expect(recoveryHeadline(undefined)).toBeUndefined();
  });

  it("says why an operator is not offered the button", () => {
    expect(withheldReason(on, true, "recovery")).toContain("identity provider");
    expect(withheldReason({ ...on, canDisable: false, cannotDisableReason: "needs the operator role" }, true, "oidc")).toBe("needs the operator role");
    expect(withheldReason(on, true, "oidc")).toBeUndefined();
    expect(withheldReason(off, true, "oidc")).toBeUndefined();
  });

  it("says when the configuration overrode an earlier turn-off", () => {
    const superseded: Recovery = { ...on, supersededAt: new Date(Date.UTC(2026, 9, 1, 9, 0)), supersededBy: "ada@example.com", generation: "2026-11" };
    expect(supersededNotice(superseded)).toContain("2026-11");
    expect(supersededNotice(off)).toBeUndefined();
    expect(supersededNotice(on)).toBeUndefined();
  });
});
