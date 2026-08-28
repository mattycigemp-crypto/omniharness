/**
 * Keyboard input normalization for the chat.
 *
 * Two problems make raw byte handling necessary instead of Ink's `useInput`:
 *
 * 1. Windows ConPTY sends DEL (0x7f) for the Backspace key. Ink labels 0x7f
 *    as `delete`, so an app that trusts `key.delete` deletes forward — a no-op
 *    at the end of the line. We intercept 0x7f on raw stdin and treat it as
 *    backspace, while the real Delete key (`ESC[3~` / kitty `ESC[3u`) keeps
 *    deleting forward.
 *
 * 2. The kitty keyboard protocol (pushed via `CSI > 1 u`) re-encodes Enter,
 *    Tab, Backspace, Escape, arrows, Home/End, Delete and ctrl+letters as
 *    `CSI keycode ; modifier u` sequences. Ink 5.x misparses most of them:
 *    some arrive as empty input with bogus ctrl/meta flags, others as raw text
 *    like `[13;2u`. `parseRawKey` recovers their meaning from the raw chunk.
 *
 * Plain text, paste and legacy (non-kitty) keys like `\r`, `\n`, `\x08`,
 * `ESC[A` arrows and ctrl+letters are left to Ink's `useInput`.
 */

export type KeyAction =
  | { kind: 'submit' }
  | { kind: 'newline' }
  | { kind: 'backspace' }
  | { kind: 'delete' }
  | { kind: 'left' }
  | { kind: 'right' }
  | { kind: 'up' }
  | { kind: 'down' }
  | { kind: 'home' }
  | { kind: 'end' }
  | { kind: 'escape' }
  | { kind: 'tab' }
  | { kind: 'ctrlC' }
  | { kind: 'ctrlM' }
  | { kind: 'ctrlO' }
  | { kind: 'ctrlE' };

/** Enable the kitty keyboard protocol (disambiguate escape codes). */
export const KITTY_PUSH = '\x1b[>1u';
/** Restore the previous keyboard state. */
export const KITTY_POP = '\x1b[<1u';
/** Query the terminal for its current progressive-enhancement flags. */
export const KITTY_QUERY = '\x1b[?u';

/**
 * True when `chunk` is the terminal's answer to the kitty-protocol query
 * (`ESC[? flags u`), confirming the protocol is actually active. Terminals
 * without kitty support ignore the query and answer nothing.
 */
export function isKittyQueryResponse(chunk: string): boolean {
  return /^\x1b\[\?\d+(?:;\d+)*u$/.test(chunk);
}

/**
 * Map a raw stdin chunk to a semantic key action, or null when it is plain
 * text or a key `useInput` already handles correctly (legacy arrows, `\r`,
 * `\n`, `\x08`, ctrl+letters, …).
 */
export function parseRawKey(chunk: string): KeyAction | null {
  if (chunk === '\x7f') return { kind: 'backspace' }; // Windows ConPTY Backspace

  if (!chunk.startsWith('\x1b')) return null;
  const body = chunk.slice(1);

  // Legacy sequences Ink drops from its Key object or mislabels.
  switch (body) {
    case 'H': case '[H': case 'OH': case '[1~': case '[7~': return { kind: 'home' };
    case 'F': case '[F': case 'OF': case '[4~': case '[8~': return { kind: 'end' };
    case '[3~': return { kind: 'delete' };
    case '[27u': return { kind: 'escape' };
    case '[9u': case '[9;2u': case '[9;5u': return { kind: 'tab' };
  }

  // Kitty protocol: CSI keycode ; modifier u.
  const kitty = /^\[(\d+)(?:;(\d+))?u$/.exec(body);
  if (kitty) {
    const code = Number(kitty[1]);
    const modifier = kitty[2] === undefined ? 1 : Number(kitty[2]);
    if (code === 13) return modifier === 1 ? { kind: 'submit' } : { kind: 'newline' }; // Enter / Shift+Ctrl+Alt+Enter
    if (code === 27) return { kind: 'escape' };
    if (code === 9) return { kind: 'tab' };
    if (code === 127) return { kind: 'backspace' };
    if (code === 3) return { kind: 'delete' };
    if (code === 11) return { kind: 'left' };
    if (code === 12) return { kind: 'right' };
    if (code === 7) return { kind: 'up' };
    if (code === 8) return { kind: 'down' };
    if (code === 1) return { kind: 'home' };
    if (code === 4) return { kind: 'end' };
    if (code === 99 && modifier === 5) return { kind: 'ctrlC' }; // ctrl+c
    if (code === 109 && modifier === 5) return { kind: 'ctrlM' }; // ctrl+m (kitty-only: legacy Ctrl+M is \r)
    if (code === 111 && modifier === 5) return { kind: 'ctrlO' }; // ctrl+o
    if (code === 101 && modifier === 5) return { kind: 'ctrlE' }; // ctrl+e
    return null; // other kitty-encoded keys (letters, F-keys, …) — not bound
  }
  return null;
}

/**
 * True when `value` is a CSI-u encoded key or protocol response (kitty key
 * events, push/pop echoes, query answers), never plain text to insert.
 */
export function isEncodedKey(value: string): boolean {
  return /^\[\d+(?:;\d+)*u$|^\[[><?]\d+(?:;\d+)*u$/.test(value);
}
