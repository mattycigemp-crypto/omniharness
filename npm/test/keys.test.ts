import assert from 'node:assert/strict';
import { test } from 'node:test';
import { KITTY_POP, KITTY_PUSH, KITTY_QUERY, isEncodedKey, isKittyQueryResponse, parseRawKey } from '../src/ui/keys.js';

test('Windows ConPTY Backspace (0x7f) maps to backspace', () => {
  assert.deepEqual(parseRawKey('\x7f'), { kind: 'backspace' });
});

test('legacy Home/End and Delete escape sequences map correctly', () => {
  assert.deepEqual(parseRawKey('\x1b[H'), { kind: 'home' });
  assert.deepEqual(parseRawKey('\x1b[F'), { kind: 'end' });
  assert.deepEqual(parseRawKey('\x1b[1~'), { kind: 'home' });
  assert.deepEqual(parseRawKey('\x1b[4~'), { kind: 'end' });
  assert.deepEqual(parseRawKey('\x1b[7~'), { kind: 'home' });
  assert.deepEqual(parseRawKey('\x1b[8~'), { kind: 'end' });
  assert.deepEqual(parseRawKey('\x1bOH'), { kind: 'home' });
  assert.deepEqual(parseRawKey('\x1bOF'), { kind: 'end' });
  assert.deepEqual(parseRawKey('\x1b[3~'), { kind: 'delete' });
});

test('kitty-protocol Enter variants map to submit or newline', () => {
  assert.deepEqual(parseRawKey('\x1b[13u'), { kind: 'submit' });
  assert.deepEqual(parseRawKey('\x1b[13;2u'), { kind: 'newline' }); // Shift+Enter
  assert.deepEqual(parseRawKey('\x1b[13;5u'), { kind: 'newline' }); // Ctrl+Enter
  assert.deepEqual(parseRawKey('\x1b[13;3u'), { kind: 'newline' }); // Alt+Enter
});

test('kitty-protocol editing keys map correctly', () => {
  assert.deepEqual(parseRawKey('\x1b[127u'), { kind: 'backspace' });
  assert.deepEqual(parseRawKey('\x1b[3u'), { kind: 'delete' });
  assert.deepEqual(parseRawKey('\x1b[11u'), { kind: 'left' });
  assert.deepEqual(parseRawKey('\x1b[12u'), { kind: 'right' });
  assert.deepEqual(parseRawKey('\x1b[7u'), { kind: 'up' });
  assert.deepEqual(parseRawKey('\x1b[8u'), { kind: 'down' });
  assert.deepEqual(parseRawKey('\x1b[1u'), { kind: 'home' });
  assert.deepEqual(parseRawKey('\x1b[4u'), { kind: 'end' });
  assert.deepEqual(parseRawKey('\x1b[1;5u'), { kind: 'home' }); // Ctrl+Home
  assert.deepEqual(parseRawKey('\x1b[4;2u'), { kind: 'end' }); // Shift+End
});

test('kitty-protocol escape, tab and ctrl letters map correctly', () => {
  assert.deepEqual(parseRawKey('\x1b[27u'), { kind: 'escape' });
  assert.deepEqual(parseRawKey('\x1b[9u'), { kind: 'tab' });
  assert.deepEqual(parseRawKey('\x1b[9;5u'), { kind: 'tab' }); // Ctrl+Tab
  assert.deepEqual(parseRawKey('\x1b[99;5u'), { kind: 'ctrlC' });
  assert.deepEqual(parseRawKey('\x1b[109;5u'), { kind: 'ctrlM' });
  assert.deepEqual(parseRawKey('\x1b[111;5u'), { kind: 'ctrlO' });
  assert.deepEqual(parseRawKey('\x1b[101;5u'), { kind: 'ctrlE' }); // kitty ctrl+e (mode cycle on legacy terminals)
});

test('plain text and keys handled by useInput return null', () => {
  assert.equal(parseRawKey('H'), null);
  assert.equal(parseRawKey('a'), null);
  assert.equal(parseRawKey('\r'), null); // Enter — useInput handles
  assert.equal(parseRawKey('\n'), null); // bare LF / Ctrl+J — useInput handles
  assert.equal(parseRawKey('\x08'), null); // legacy backspace — useInput handles
  assert.equal(parseRawKey('\x1b[A'), null); // legacy up arrow — useInput handles
  assert.equal(parseRawKey('\x1b[65u'), null); // kitty-encoded 'a' — not bound
  assert.equal(parseRawKey('\x1b[5u'), null); // kitty PageUp — not bound
  assert.equal(parseRawKey(''), null);
  assert.equal(parseRawKey('hello'), null);
});

test('distinguishes unrecognized encoded keys from plain text', () => {
  assert.equal(isEncodedKey('[13;2u'), true);
  assert.equal(isEncodedKey('[65u'), true); // kitty-encoded 'a'
  assert.equal(isEncodedKey('[>1u'), true); // push echo
  assert.equal(isEncodedKey('[<1u'), true); // pop echo
  assert.equal(isEncodedKey('[?1u'), true); // query answer
  assert.equal(isEncodedKey('[?1;2u'), true);
  assert.equal(isEncodedKey('hello [note]'), false);
  assert.equal(isEncodedKey('a'), false);
  assert.equal(isEncodedKey(''), false);
});

test('push, pop and query sequences are distinct', () => {
  assert.equal(KITTY_PUSH, '\x1b[>1u');
  assert.equal(KITTY_POP, '\x1b[<1u');
  assert.equal(KITTY_QUERY, '\x1b[?u');
});

test('isKittyQueryResponse recognizes the terminal kitty answer', () => {
  assert.equal(isKittyQueryResponse('\x1b[?1u'), true);
  assert.equal(isKittyQueryResponse('\x1b[?2u'), true);
  assert.equal(isKittyQueryResponse('\x1b[?1;2u'), true);
  assert.equal(isKittyQueryResponse('\x1b[?1;2c'), false); // DA1 device-attributes answer
  assert.equal(isKittyQueryResponse('\x1b[>1u'), false); // push echo is not a query answer
  assert.equal(isKittyQueryResponse('\x1b[13;2u'), false); // key event
  assert.equal(isKittyQueryResponse('hello'), false);
  assert.equal(isKittyQueryResponse(''), false);
});
