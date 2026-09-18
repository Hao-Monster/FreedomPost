export interface ChatwootReadyWidget {
  hasLoaded?: boolean;
}

export type ChatwootReadyListener = () => void;
export type ChatwootReadySubscription = (listener: ChatwootReadyListener) => () => void;

export interface ChatwootReadyOptions {
  timeoutMs?: number;
  pollMs?: number;
}

/**
 * Wait until the Chatwoot iframe has completed its SDK handshake.
 *
 * The SDK publishes `$chatwoot` before the iframe is ready. Calling `toggle`
 * during that gap can set `isOpen` without delivering the open message to the
 * iframe, leaving the first automatically opened widget at height 0. The
 * ready event handles the normal path; bounded polling also covers hosts that
 * load the SDK before this listener is attached.
 */
export function waitForChatwootReady<T extends ChatwootReadyWidget>(
  getWidget: () => T | undefined,
  subscribe: ChatwootReadySubscription,
  options: ChatwootReadyOptions = {}
): Promise<T> {
  const timeoutMs = normalizeDelay(options.timeoutMs, 5000);
  const pollMs = normalizeDelay(options.pollMs, 100);

  return new Promise<T>((resolve, reject) => {
    const initiallyLoaded = getWidget();
    if (initiallyLoaded?.hasLoaded === true) {
      resolve(initiallyLoaded);
      return;
    }

    let settled = false;
    let pollTimer: ReturnType<typeof setTimeout> | undefined;
    let timeoutTimer: ReturnType<typeof setTimeout> | undefined;
    let unsubscribe = () => {};
    let cleanupRequested = false;

    const cleanup = () => {
      if (pollTimer !== undefined) clearTimeout(pollTimer);
      if (timeoutTimer !== undefined) clearTimeout(timeoutTimer);
      cleanupRequested = true;
      try {
        unsubscribe();
      } catch {
        // Listener cleanup must not change the readiness result.
      }
    };

    const succeed = (widget: T) => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve(widget);
    };

    const fail = () => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(new Error("chatwoot-not-ready"));
    };

    const check = () => {
      if (settled) return;
      const widget = getWidget();
      if (widget?.hasLoaded === true) {
        succeed(widget);
        return;
      }
      if (pollTimer !== undefined) clearTimeout(pollTimer);
      pollTimer = setTimeout(check, pollMs);
    };

    const onReady = () => {
      const widget = getWidget();
      if (widget) {
        succeed(widget);
      } else {
        check();
      }
    };

    timeoutTimer = setTimeout(fail, timeoutMs);
    try {
      const removeListener = subscribe(onReady);
      unsubscribe = removeListener;
      if (cleanupRequested) removeListener();
    } catch {
      fail();
      return;
    }

    check();
  });
}

function normalizeDelay(value: number | undefined, fallback: number): number {
  return Number.isFinite(value) && (value as number) >= 0 ? (value as number) : fallback;
}
