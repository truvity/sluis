import { describe, expect, it } from "vitest";

import { paths } from "./router";
import { viaTarget } from "./viaModel";

describe("where a holder's reason leads", () => {
  it("sends a provider group to its page", () => {
    expect(viaTarget("eng@tsarev.id")).toBe(paths.directoryGroup("eng@tsarev.id"));
  });

  it("never sends a matcher's words to a provider group", () => {
    expect(viaTarget("signed in at tsarev.id")).toBe("/people?domain=tsarev.id");
    expect(viaTarget("signed in at tsarev.id")).not.toContain("directory-groups");
    expect(viaTarget("signed in as ann@tsarev.id")).toBe(paths.person("ann@tsarev.id"));
  });

  it("links nothing for a workload matcher", () => {
    expect(viaTarget("CI job with repository acme/x")).toBeUndefined();
    expect(viaTarget("ServiceAccount a/b on c")).toBeUndefined();
    expect(viaTarget("AWS role x")).toBeUndefined();
  });
});
