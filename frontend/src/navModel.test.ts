import { describe, expect, it } from "vitest";

import { clusters } from "./navModel";
import { parse } from "./router";

const labels = (options: { sessions: boolean; audit: boolean; cloudflare?: boolean }) => clusters(options).map((c) => [c.heading ?? "", c.entries.map((e) => e.label)]);

describe("clusters", () => {
  it("lays the rail out as Overview and four clusters", () => {
    expect(labels({ sessions: true, audit: true })).toEqual([
      ["", ["Overview"]],
      ["Identity", ["Directories", "Directory groups", "People", "Rules"]],
      ["Access", ["Internal groups", "Clients", "Sessions"]],
      ["Systems", ["GitHub", "Slack"]],
      ["Admin", ["Audit", "Settings"]],
    ]);
  });

  it("leaves out what the caller cannot use", () => {
    expect(labels({ sessions: false, audit: false })).toEqual([
      ["", ["Overview"]],
      ["Identity", ["Directories", "Directory groups", "People", "Rules"]],
      ["Access", ["Internal groups", "Clients"]],
      ["Systems", ["GitHub", "Slack"]],
      ["Admin", ["Settings"]],
    ]);
  });

  it("shows Cloudflare under Systems only where sluis mints for it", () => {
    expect(labels({ sessions: false, audit: false, cloudflare: true })[3]).toEqual(["Systems", ["GitHub", "Slack", "Cloudflare"]]);
    expect(labels({ sessions: false, audit: false })[3]).toEqual(["Systems", ["GitHub", "Slack"]]);
  });

  it("has one entry for Slack and none for its old pages", () => {
    const values = clusters({ sessions: true, audit: true }).flatMap((c) => c.entries.map((e) => e.value));
    expect(values).toContain("slack");
    expect(values).not.toContain("slack-apps");
    expect(values).not.toContain("slack-connect");
  });

  it("points every entry at a route that opens its own view", () => {
    for (const entry of clusters({ sessions: true, audit: true, cloudflare: true }).flatMap((c) => c.entries)) {
      expect(parse(`#${entry.to}`).view).toBe(entry.value);
    }
  });
});
