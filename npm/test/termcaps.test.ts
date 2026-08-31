import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough } from 'node:stream';
import { SYNC_BEGIN, SYNC_END, isSyncOutputReply, osc9Notify, osc52Copy, shouldNudgeOnFinish, wrapSynchronizedOutput } from '../src/ui/termcaps.js';

test('sync-output reply is recognised for every DECRPM state', () => {
  assert.equal(isSyncOutputReply('\x1b[?2026;1$y'), true);
  assert.equal(isSyncOutputReply('\x1b[?2026;2$y'), true);
  assert.equal(isSyncOutputReply('\x1b[?2026;0$y'), true);
  assert.equal(isSyncOutputReply('\x1b[?1u'), false);
  assert.equal(isSyncOutputReply(''), false);
});

test('wrapSynchronizedOutput brackets string writes and restores cleanly', () => {
  const stream = new PassThrough();
  const seen: string[] = [];
  stream.on('data', (chunk: Buffer) => seen.push(chunk.toString()));
  const restore = wrapSynchronizedOutput(stream as unknown as { write: (c: unknown, ...r: unknown[]) => boolean });
  stream.write('frame');
  restore();
  stream.write('after');
  assert.equal(seen[0], `${SYNC_BEGIN}frame${SYNC_END}`);
  assert.equal(seen[1], 'after');
});

test('osc52Copy base64-encodes and refuses oversized payloads', () => {
  assert.equal(osc52Copy('hi'), `\x1b]52;c;${Buffer.from('hi').toString('base64')}\x07`);
  assert.equal(osc52Copy('x'.repeat(200_000)), null);
});

test('osc9Notify strips control chars and terminates with BEL', () => {
  const seq = osc9Notify('done\nnow');
  assert.equal(seq.startsWith('\x1b]9;'), true);
  assert.equal(seq.endsWith('\x07'), true);
  assert.equal(seq.includes('\n'), false);
});

test('shouldNudgeOnFinish gates on elapsed time and focus', () => {
  assert.equal(shouldNudgeOnFinish(20_000, false), true);
  assert.equal(shouldNudgeOnFinish(20_000, true), false);
  assert.equal(shouldNudgeOnFinish(2_000, false), false);
});
