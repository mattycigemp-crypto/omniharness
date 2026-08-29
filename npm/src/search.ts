/** Pure cosine-similarity + text chunking helpers for the workspace semantic index. */

export interface IndexedChunk {
  path: string;
  text: string;
  embedding: number[];
}

/** Cosine similarity of two equal-length vectors; 0 for empty vectors. */
export function cosineSimilarity(a: readonly number[], b: readonly number[]): number {
  if (a.length === 0 || a.length !== b.length) return 0;
  let dot = 0;
  let normA = 0;
  let normB = 0;
  for (let i = 0; i < a.length; i += 1) {
    dot += a[i]! * b[i]!;
    normA += a[i]! * a[i]!;
    normB += b[i]! * b[i]!;
  }
  if (normA === 0 || normB === 0) return 0;
  return dot / (Math.sqrt(normA) * Math.sqrt(normB));
}

/** Split text into overlapping chunks on word boundaries, at most `maxLength` chars each. */
export function chunkText(text: string, maxLength = 800): string[] {
  const chunks: string[] = [];
  const clean = text.replace(/\s+/g, ' ').trim();
  if (clean === '') return chunks;
  let start = 0;
  while (start < clean.length) {
    if (clean.length - start <= maxLength) {
      chunks.push(clean.slice(start));
      break;
    }
    const space = clean.lastIndexOf(' ', start + maxLength);
    if (space > start) {
      chunks.push(clean.slice(start, space));
      start = space + 1;
    } else {
      chunks.push(clean.slice(start, start + maxLength));
      start += maxLength;
    }
  }
  return chunks;
}
