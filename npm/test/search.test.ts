import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { chunkText, cosineSimilarity } from '../src/search.js';

describe('cosineSimilarity', () => {
  it('returns 1 for identical vectors', () => {
    assert.equal(cosineSimilarity([1, 2, 3], [1, 2, 3]), 1);
  });

  it('returns 0 for orthogonal vectors', () => {
    assert.ok(Math.abs(cosineSimilarity([1, 0], [0, 1])) < 1e-9);
  });

  it('returns -1 for opposite vectors', () => {
    assert.ok(Math.abs(cosineSimilarity([1, 0], [-1, 0]) + 1) < 1e-9);
  });

  it('returns 0 for empty or mismatched vectors', () => {
    assert.equal(cosineSimilarity([], []), 0);
    assert.equal(cosineSimilarity([1], [1, 2]), 0);
  });

  it('returns 0 when a vector is all zeros', () => {
    assert.equal(cosineSimilarity([0, 0], [1, 1]), 0);
  });

  it('ranks a closer match above a farther one', () => {
    const query = [1, 0];
    const close = [0.9, 0.1];
    const far = [0.1, 0.9];
    assert.ok(cosineSimilarity(query, close) > cosineSimilarity(query, far));
  });
});

describe('chunkText', () => {
  it('returns [] for empty or whitespace-only text', () => {
    assert.deepEqual(chunkText(''), []);
    assert.deepEqual(chunkText('   \n\t '), []);
  });

  it('keeps short text as a single chunk', () => {
    assert.deepEqual(chunkText('hello world'), ['hello world']);
  });

  it('splits long text on word boundaries within the limit', () => {
    const text = 'one two three four five six seven eight nine ten';
    const chunks = chunkText(text, 12);
    assert.ok(chunks.length > 1);
    for (const chunk of chunks) assert.ok(chunk.length <= 12, `chunk over limit: ${chunk}`);
    assert.equal(chunks.join(' ').replace(/\s+/g, ' ').trim(), text);
  });

  it('hard-slices a single word longer than the limit', () => {
    const chunks = chunkText('abcdefghijklmnop', 8);
    assert.equal(chunks.length, 2);
    assert.equal(chunks[0], 'abcdefgh');
    assert.equal(chunks[1], 'ijklmnop');
  });
});
