import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const here = path.dirname(fileURLToPath(import.meta.url));
const cli = path.join(here, '..', 'src', 'cli.tsx');

// The alternate screen buffer has no scrollback — that is its purpose. While
// the harness ran in it you could not scroll back to anything: not a file read
// three steps ago, not the reasoning behind an edit. It also defeated the
// <Static> region in the TUI, which exists precisely to push settled turns into
// scrollback and had nowhere to push them.
//
// This is a one-line change to reintroduce and invisible in every test that
// renders into a fake stream, so it is asserted against the source.
test('the TUI does not run in the alternate screen buffer', async () => {
  const source = await readFile(cli, 'utf8');
  for (const sequence of ['?1049h', '?1049l', 'smcup', 'rmcup']) {
    assert.ok(
      !source.includes(sequence),
      `cli.tsx switches to the alternate screen (${sequence}), which removes scrollback`,
    );
  }
});

test('the transcript is not dumped again on exit', async () => {
  // Scrollback already holds it, so printing it again on quit duplicated every
  // line. The two go together: bring back one and you want the other.
  const source = await readFile(cli, 'utf8');
  assert.ok(!source.includes('dumpTranscript'), 'the exit-time transcript dump is redundant now');
});
