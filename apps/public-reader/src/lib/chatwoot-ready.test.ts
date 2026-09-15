import { describe, expect, it, vi } from "vitest";
import { waitForChatwootReady, type ChatwootReadyWidget } from "./chatwoot-ready.js";

describe("waitForChatwootReady", () => {
  it("resolves immediately when the widget is already loaded", async () => {
    const widget: ChatwootReadyWidget = { hasLoaded: true };
    const unsubscribe = vi.fn();
    const subscribe = vi.fn(() => unsubscribe);

    await expect(waitForChatwootReady(() => widget, subscribe)).resolves.toBe(widget);
    expect(subscribe).not.toHaveBeenCalled();
    expect(unsubscribe).not.toHaveBeenCalled();
  });

  it("resolves from the SDK ready event and stops polling", async () => {
    vi.useFakeTimers();
    try {
      let widget: ChatwootReadyWidget = { hasLoaded: false };
      let ready!: () => void;
      const unsubscribe = vi.fn();
      const promise = waitForChatwootReady(
        () => widget,
        (listener) => {
          ready = listener;
          return unsubscribe;
        },
        { timeoutMs: 5000, pollMs: 1000 }
      );

      widget = { hasLoaded: true };
      ready();
      await expect(promise).resolves.toBe(widget);
      expect(unsubscribe).toHaveBeenCalledOnce();
      await vi.advanceTimersByTimeAsync(5000);
    } finally {
      vi.useRealTimers();
    }
  });

  it("resolves when polling observes the loaded state", async () => {
    vi.useFakeTimers();
    try {
      let loaded = false;
      const promise = waitForChatwootReady(
        () => ({ hasLoaded: loaded }),
        () => () => {},
        { timeoutMs: 1000, pollMs: 100 }
      );

      loaded = true;
      await vi.advanceTimersByTimeAsync(100);
      await expect(promise).resolves.toEqual({ hasLoaded: true });
    } finally {
      vi.useRealTimers();
    }
  });

  it("cleans up a listener when it invokes the ready callback synchronously", async () => {
    const unsubscribe = vi.fn();
    const widget: ChatwootReadyWidget = { hasLoaded: false };
    const promise = waitForChatwootReady(
      () => widget,
      (listener) => {
        listener();
        return unsubscribe;
      }
    );

    await expect(promise).resolves.toBe(widget);
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("treats the SDK ready event as authoritative", async () => {
    let ready!: () => void;
    const widget: ChatwootReadyWidget = { hasLoaded: false };
    const promise = waitForChatwootReady(
      () => widget,
      (listener) => {
        ready = listener;
        return () => {};
      },
      { timeoutMs: 1000, pollMs: 100 }
    );

    ready();
    await expect(promise).resolves.toBe(widget);
  });

  it("rejects after the bounded timeout and removes the listener", async () => {
    vi.useFakeTimers();
    try {
      const unsubscribe = vi.fn();
      const promise = waitForChatwootReady(
        () => ({ hasLoaded: false }),
        () => unsubscribe,
        { timeoutMs: 250, pollMs: 50 }
      );

      const rejection = expect(promise).rejects.toThrow("chatwoot-not-ready");
      await vi.advanceTimersByTimeAsync(250);
      await rejection;
      expect(unsubscribe).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });
});
