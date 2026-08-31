/**
 * Terminal capability primitives — zero-dependency escape sequences that
 * degrade to nothing on terminals that don't support them.
 *
 * Pure helpers plus two small stateful wrappers (`wrapSynchronizedOutput`,
 * `isSyncOutputReply`). Nothing here assumes a capability: callers probe
 * first (like the kitty-protocol query in `keys.ts`) and only enable a
 * feature once the terminal answers.
 *
 *   - Synchronized Output (DECSET 2026): bracket each frame so the terminal
 *     composites the whole update at once instead of tearing mid-repaint.
 *   - OSC 52: write the system clipboard (works over SSH and tmux). Most
 *     terminals allow writes and refuse reads, so this is write-only.
 *   - OSC 9: fire a desktop notification when a long run finishes.
 *   - BEL: the terminal bell, gated by the caller to "long + unfocused".
 */

/** DECSET 2026 — begin a synchronized (tear-free) frame. */
export const SYNC_BEGIN = '\x1b[?2026h';
/** DECRST 2026 — end the synchronized frame; terminal flushes it atomically. */
export const SYNC_END = '\x1b[?2026l';
/** DECRQM 2026 — ask whether synchronized output is supported. */
export const SYNC_QUERY = '\x1b[?2026$p';

/**
 * True when `chunk` is the terminal's answer to {@link SYNC_QUERY}
 * (`ESC [ ? 2026 ; <state> $ y`). State 1 = set, 2 = reset, 3/4 = permanently
 * set/reset — any of them means the mode is recognised, which is all we need.
 */
export function isSyncOutputReply(chunk: string): boolean {
  return /\x1b\[\?2026;[0-4]\$y/.test(chunk);
}

/**
 * Wrap a writable stream so every string write is bracketed by the
 * synchronized-output markers. Ink emits one `write()` per rendered frame, so
 * this makes each frame tear-free without hooking React's commit phase.
 * Returns a restore function. Non-string chunks (rare from Ink) pass through
 * untouched so binary writes are never corrupted.
 */
export function wrapSynchronizedOutput(stream: { write: (chunk: unknown, ...rest: unknown[]) => boolean }): () => void {
  const original = stream.write.bind(stream);
  stream.write = (chunk: unknown, ...rest: unknown[]): boolean => {
    if (typeof chunk === 'string' && chunk.length > 0) {
      return original(`${SYNC_BEGIN}${chunk}${SYNC_END}`, ...rest);
    }
    return original(chunk, ...rest);
  };
  return () => { stream.write = original; };
}

/**
 * OSC 52 clipboard-write sequence for `text`. Base64 is required by the
 * protocol. Terminals cap the payload (commonly ~74–100 KB before the encode);
 * we refuse anything that would obviously exceed that rather than emit a
 * sequence the terminal will silently drop.
 */
export function osc52Copy(text: string): string | null {
  const encoded = Buffer.from(text, 'utf8').toString('base64');
  if (encoded.length > 96_000) return null;
  return `\x1b]52;c;${encoded}\x07`;
}

/** OSC 9 desktop-notification sequence (iTerm2, WezTerm, Ghostty, Windows Terminal). */
export function osc9Notify(title: string): string {
  // Strip control characters; the sequence is terminated by BEL.
  return `\x1b]9;${title.replace(/[\x00-\x1f\x7f]/g, ' ').slice(0, 200)}\x07`;
}

/** The terminal bell. Caller decides when it's warranted. */
export const BEL = '\x07';

/**
 * Whether a "run finished" nudge (bell + notification) is warranted: the run
 * took long enough to have caused a context switch, and the terminal isn't
 * the focused window. Focus is unknowable without extra querying, so callers
 * pass what they have; default is "assume unfocused after a long run".
 */
export function shouldNudgeOnFinish(elapsedMs: number, focused = false, thresholdMs = 10_000): boolean {
  return elapsedMs >= thresholdMs && !focused;
}
