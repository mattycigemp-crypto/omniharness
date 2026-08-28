/**
 * Keyboard input normalization for the chat.
 *
 * Terminals cannot distinguish Shift+Enter from Enter by default: both send
 * `\r`. Capable terminals (kitty, foot, WezTerm, Ghostty, modern Windows
 * Terminal, …) fix this with the kitty keyboard protocol — the app pushes
 * `CSI > 1 u` and the terminal then encodes colliding keys as `CSI keycode ;
 * modifier u` (Shift+Enter = `ESC[13;2u`). Ink 5.x does not parse those
 * sequences; they reach `useInput` as plain text with the leading ESC
 * stripped. This module recovers their meaning, and also maps the bare LF
 * some terminals send for Shift+Enter.
 */

export type TranslatedKey =
  | { kind: 'submit' }
  | { kind: 'newline' }
  | { kind: 'escape' }
  | { kind: 'tab' }
  | { kind: 'ctrlC' }
  | { kind: 'ctrlM' }
  | { kind: 'ctrlO' };

/** Enable the kitty keyboard protocol (disambiguate escape codes). */
export const KITTY_PUSH = '\x1b[>1u';
/** Restore the previous keyboard state. */
export const KITTY_POP = '\x1b[<1u';

/**
 * Translate a CSI-u value (ESC already stripped by Ink) into a semantic key,
 * or null when it is a plain character. Returns null for encoded sequences we
 * do not act on (unknown keys) — callers should not insert those as text.
 */
export function translateCsiU(value: string): TranslatedKey | null {
  const enter = /^\[13(?:;(\d+))?u$/.exec(value);
  if (enter) {
    const modifier = enter[1] === undefined ? 1 : Number(enter[1]);
    return modifier === 1 ? { kind: 'submit' } : { kind: 'newline' };
  }
  switch (value) {
    case '[27u': return { kind: 'escape' };
    case '[9u':
    case '[9;2u':
    case '[9;5u':
      return { kind: 'tab' };
    case '[99;5u': return { kind: 'ctrlC' };
    case '[109;5u': return { kind: 'ctrlM' };
    case '[111;5u': return { kind: 'ctrlO' };
    default: return null;
  }
}

/** True when `value` is an unrecognized CSI-u encoded key, never plain text. */
export function isEncodedKey(value: string): boolean {
  return /^\[\d+(?:;\d+)*u$/.test(value);
}

export type RawKey = 'home' | 'end';

/**
 * Detect Home/End from a raw stdin chunk. Ink's `useInput` drops these keys
 * (its Key object has no home/end flags), so we listen on stdin directly.
 * Handles the legacy sequences (xterm `ESC[H`/`ESC[F`, rxvt `ESC[1~`/`ESC[4~`,
 * `ESC[7~`/`ESC[8~`) and the kitty-protocol forms (`ESC[1u`/`ESC[4u` with any
 * modifier). Returns null for anything that is not exactly a Home/End key.
 */
export function parseRawKey(chunk: string): RawKey | null {
  if (!chunk.startsWith('\x1b')) return null;
  const body = chunk.slice(1);
  if (body === 'H' || body === '[H' || body === 'OH' || body === '[1~' || body === '[7~' || /^\[1(?:;\d+)?u$/.test(body)) return 'home';
  if (body === 'F' || body === '[F' || body === 'OF' || body === '[4~' || body === '[8~' || /^\[4(?:;\d+)?u$/.test(body)) return 'end';
  return null;
}
