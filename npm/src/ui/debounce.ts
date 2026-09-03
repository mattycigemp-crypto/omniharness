/**
 * Trailing-edge debounce: calls `fn` once, `delayMs` after the last call, and
 * drops every call in between. Exists for one caller — the terminal's
 * `resize` event — where several calls arrive in a burst and only the last
 * one is worth acting on.
 */
export interface Debounced<A extends unknown[]> {
  (...args: A): void;
  /** Drop a pending call. Use on cleanup so a resize from before unmount does
   *  not fire setState after the component is gone. */
  cancel(): void;
}

export function debounce<A extends unknown[]>(fn: (...args: A) => void, delayMs: number): Debounced<A> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const debounced = ((...args: A) => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      fn(...args);
    }, delayMs);
  }) as Debounced<A>;
  debounced.cancel = () => {
    if (timer) clearTimeout(timer);
    timer = undefined;
  };
  return debounced;
}
