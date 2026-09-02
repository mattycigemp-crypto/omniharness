/**
 * Casts for the TTY streams Ink renders into.
 *
 * NodeJS.ReadStream and WriteStream describe a real terminal — cursorTo,
 * clearScreenDown, isRaw and some seventy other members — and a fake that
 * implemented all of them would be a terminal emulator rather than a test
 * helper. Ink touches only a handful.
 *
 * So the claim "this stands in for a terminal" is made once, here, next to the
 * reason for it, rather than as an unexplained cast at every render call.
 */
export const asStdin = (stream: unknown): NodeJS.ReadStream => stream as NodeJS.ReadStream;
export const asStdout = (stream: unknown): NodeJS.WriteStream => stream as NodeJS.WriteStream;
