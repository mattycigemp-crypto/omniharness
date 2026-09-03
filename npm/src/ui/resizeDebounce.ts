import { debounce } from './debounce.js';

type Listener = (...args: unknown[]) => void;

/**
 * Coalesce `resize` event delivery on a TTY stream so every listener — Ink's
 * own internal one included — sees one event per burst instead of one per raw
 * OS-level tick.
 *
 * Ink attaches its own `resize` listener directly to whatever stream is
 * handed to `render()`, and on every single tick it recalculates its Yoga
 * layout and writes a fresh frame — independent of anything application state
 * does. That is not reachable by debouncing a `useState` call in a component:
 * the redraw happens inside Ink regardless.
 *
 * A maximise or restore on Windows Terminal does not deliver one resize
 * event. It animates through the transition and fires a burst of
 * intermediate sizes a few milliseconds apart. Reacting to each one means
 * Ink redraws against a size that is already stale by the time the escape
 * sequences reach the terminal, and the frames land on top of one another
 * instead of replacing one another — duplicated prompt boxes, fragments of an
 * earlier frame still on screen once the window settles.
 *
 * The only place to fix that is before Ink's own listener ever sees the
 * event — so this patches `.on` / `.off` for `resize` specifically, on the
 * same stream object Ink and application code both subscribe to. `write`,
 * `columns`, `rows`, `isTTY` and every other event pass through untouched.
 */
export function debounceResizeEvents<S extends NodeJS.WriteStream>(stream: S, delayMs: number): S {
  const realOn = stream.on.bind(stream);
  const realOff = stream.off.bind(stream);
  // Keyed on the caller's own listener, so .off(listener) still finds and
  // cancels the right debounced wrapper — the caller never sees the wrapper.
  const wrapped = new WeakMap<Listener, ReturnType<typeof debounce<unknown[]>>>();

  stream.on = ((event: string, listener: Listener) => {
    if (event !== 'resize') return realOn(event, listener);
    const debounced = debounce(listener, delayMs);
    wrapped.set(listener, debounced);
    return realOn(event, debounced);
  }) as typeof stream.on;

  stream.off = ((event: string, listener: Listener) => {
    if (event !== 'resize') return realOff(event, listener);
    const debounced = wrapped.get(listener);
    if (debounced) {
      debounced.cancel();
      wrapped.delete(listener);
      return realOff(event, debounced);
    }
    return realOff(event, listener);
  }) as typeof stream.off;

  return stream;
}
