import { describe, expect, it } from "vitest";

import { explainPanels } from "./explainModel";

describe("explainPanels", () => {
  it("shows the person's effective access and the proof simulator to an operator", () => {
    expect(explainPanels(true)).toEqual({ person: true, simulator: true });
  });

  it("hides both from everyone else", () => {
    expect(explainPanels(false)).toEqual({ person: false, simulator: false });
  });
});
