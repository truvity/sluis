import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { debounced } from "./debounce";

describe("debounced", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("runs once after the last of a burst of calls", () => {
    const fn = vi.fn();
    const d = debounced(fn, 300);
    for (const _ of "alice") {
      d.call();
      vi.advanceTimersByTime(100);
    }
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(300);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("flush runs now and leaves nothing pending", () => {
    const fn = vi.fn();
    const d = debounced(fn, 300);
    d.call();
    d.flush();
    expect(fn).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1000);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("cancel drops a pending run", () => {
    const fn = vi.fn();
    const d = debounced(fn, 300);
    d.call();
    d.cancel();
    vi.advanceTimersByTime(1000);
    expect(fn).not.toHaveBeenCalled();
  });
});
