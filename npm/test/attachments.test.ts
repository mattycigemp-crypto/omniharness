import assert from 'node:assert/strict';
import { test } from 'node:test';
import { attachmentBlock, kindFromName } from '../src/attachments.js';

test('kindFromName classifies by extension', () => {
  assert.equal(kindFromName('photo.png'), 'image');
  assert.equal(kindFromName('shot.JPEG'), 'image');
  assert.equal(kindFromName('clip.mp4'), 'video');
  assert.equal(kindFromName('notes.txt'), 'file');
  assert.equal(kindFromName('archive.tar.gz'), 'file');
});

test('attachmentBlock formats the manifest or returns empty', () => {
  assert.equal(attachmentBlock([]), '');
  const block = attachmentBlock([
    { name: 'a.png', kind: 'image', size: 10 },
    { name: 'b.txt', kind: 'file', size: 5 },
  ]);
  assert.match(block, /- a\.png \(image, 10 bytes\)/);
  assert.match(block, /- b\.txt \(file, 5 bytes\)/);
  assert.match(block, /read_file the others/);
});
