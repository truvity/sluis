import { describe, expect, it } from "vitest";

import { hubReason, issuerReason, listView } from "./reasonModel";

describe("hubReason", () => {
  it("says the sign-in ended for the hub's own unauthenticated", () => {
    expect(hubReason(new Error("[unauthenticated] no session"))).toMatch(/sign-in here has ended/);
  });
  it("strips the code from anything else", () => {
    expect(hubReason(new Error("[not_found] no such group"))).toBe("no such group");
  });
});

describe("issuerReason", () => {
  it("words an issuer refusal as the issuer refusing, not as a lapsed sign-in", () => {
    const text = issuerReason(new Error("[unauthenticated] token rejected"));
    expect(text).toMatch(/issuer refused/);
    expect(text).toMatch(/still signed in/);
    expect(text).not.toMatch(/sign-in here has ended/);
  });
  it("passes other failures through without the code", () => {
    expect(issuerReason(new Error("[unavailable] connection refused"))).toBe("connection refused");
  });
});

describe("listView", () => {
  it("never shows empty under an error", () => {
    expect(listView({ loading: false, error: "boom", count: 0 })).toBe("error");
  });
  it("shows empty only once loaded without error", () => {
    expect(listView({ loading: true, count: 0 })).toBe("loading");
    expect(listView({ loading: false, count: 0 })).toBe("empty");
    expect(listView({ loading: false, count: 2 })).toBe("rows");
  });
});
