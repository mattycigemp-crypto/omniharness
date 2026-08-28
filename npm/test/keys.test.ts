import assert from 'node:assert/strict';
import { test } from 'node:test';
import { KITTY_POP, KITTY_PUSH, isEncodedKey, parseRawKey, translateCsiU } from '../src/ui/keys.js';

test('translates kitty-protocol Enter and Shift+Enter', () => {
  assert.deepEqual(translateCsiU('[13u'), { kind: 'submit' });
  assert.deepEqual(translateCsiU('[13;2u'), { kind: 'newline' });
  assert.deepEqual(translateCsiU('[13;5u'), { kind: 'newline' }); // Ctrl+Enter
  assert.deepEqual(translateCsiU('[13;3u'), { kind: 'newline' }); // Alt+Enter
});

test('translates kitty-protocol escape, tab and ctrl letters', () => {
  assert.deepEqual(translateCsiU('[27u'), { kind: 'escape' });
  assert.deepEqual(translateCsiU('[9u'), { kind: 'tab' });
  assert.deepEqual(translateCsiU('[99;5u'), { kind: 'ctrlC' });
  assert.deepEqual(translateCsiU('[109;5u'), { kind: 'ctrlM' });
  assert.deepEqual(translateCsiU('[111;5u'), { kind: 'ctrlO' });
});

test('leaves plain characters untouched', () => {
  assert.equal(translateCsiU('a'), null);
  assert.equal(translateCsiU(' '), null);
  assert.equal(translateCsiU(''), null);
});

test('distinguishes unrecognized encoded keys from plain text', () => {
  assert.equal(isEncodedKey('[13;2u'), true);
  assert.equal(isEncodedKey('[65u'), true); // kitty-encoded 'a'
  assert.equal(isEncodedKey('hello [note]'), false);
  assert.equal(isEncodedKey('a'), false);
  assert.equal(isEncodedKey(''), false);
});

test('push and pop sequences are distinct', () => {
  assert.equal(KITTY_PUSH, '\x1b[>1u');
  assert.equal(KITTY_POP, '\x1b[<1u');
});

test('parseRawKey detects legacy Home/End sequences', () => {
  assert.equal(parseRawKey('\x1b[H'), 'home');
  assert.equal(parseRawKey('\x1b[F'), 'end');
  assert.equal(parseRawKey('\x1b[1~'), 'home');
  assert.equal(parseRawKey('\x1b[4~'), 'end');
  assert.equal(parseRawKey('\x1b[7~'), 'home');
  assert.equal(parseRawKey('\x1b[8~'), 'end');
  assert.equal(parseRawKey('\x1bOH'), 'home');
  assert.equal(parseRawKey('\x1bOF'), 'end');
});

test('parseRawKey detects kitty-protocol Home/End with any modifier', () => {
  assert.equal(parseRawKey('\x1b[1u'), 'home');
  assert.equal(parseRawKey('\x1b[4u'), 'end');
  assert.equal(parseRawKey('\x1b[1;5u'), 'home'); // Ctrl+Home
  assert.equal(parseRawKey('\x1b[4;2u'), 'end'); // Shift+End
});

test('parseRawKey ignores plain text and other keys', () => {
  assert.equal(parseRawKey('H'), null);
  assert.equal(parseRawKey('\x1b[A'), null); // up arrow
  assert.equal(parseRawKey('\x1b[65u'), null); // kitty-encoded 'a'
  assert.equal(parseRawKey(''), null);
  assert.equal(parseRawKey('hello'), null);
});
