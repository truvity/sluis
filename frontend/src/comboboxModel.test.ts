import { describe, expect, it } from "vitest";

import { chosenIndex, nextActive } from "./comboboxModel";

describe("nextActive", () => {
  it("starts at the first option on ArrowDown and wraps", () => {
    expect(nextActive(-1, "ArrowDown", 3)).toBe(0);
    expect(nextActive(0, "ArrowDown", 3)).toBe(1);
    expect(nextActive(2, "ArrowDown", 3)).toBe(0);
  });
  it("starts at the last option on ArrowUp and wraps", () => {
    expect(nextActive(-1, "ArrowUp", 3)).toBe(2);
    expect(nextActive(0, "ArrowUp", 3)).toBe(2);
    expect(nextActive(2, "ArrowUp", 3)).toBe(1);
  });
  it("jumps with Home and End", () => {
    expect(nextActive(1, "Home", 3)).toBe(0);
    expect(nextActive(1, "End", 3)).toBe(2);
  });
  it("ignores other keys and an empty list", () => {
    expect(nextActive(1, "a", 3)).toBe(1);
    expect(nextActive(1, "ArrowDown", 0)).toBe(-1);
  });
});

describe("chosenIndex", () => {
  it("opens the first hit when the arrows were never used", () => {
    expect(chosenIndex(-1, 4)).toBe(0);
  });
  it("opens the active one", () => {
    expect(chosenIndex(2, 4)).toBe(2);
  });
  it("opens nothing when there is nothing", () => {
    expect(chosenIndex(-1, 0)).toBe(-1);
    expect(chosenIndex(5, 0)).toBe(-1);
  });
  it("falls back to the first when the list shrank under the cursor", () => {
    expect(chosenIndex(5, 3)).toBe(0);
  });
});
