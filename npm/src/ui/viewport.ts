/**
 * How much of the live region fits in the terminal.
 *
 * Ink redraws its live region by walking the cursor up over the lines it
 * wrote last time and erasing them. That accounting only holds while the
 * frame fits on screen: once it is taller than the viewport the terminal
 * scrolls it, the cursor no longer lands where Ink expects, and the redraw
 * eats the transcript above or leaves a trail of half-erased frames. It shows
 * up as the display "going weird" after a resize, because maximising and then
 * floating a window is the ordinary way to make the viewport smaller than the
 * frame that was drawn for it.
 *
 * So the sections that can grow are given a budget rather than a fixed cap.
 */

export interface ViewportInput {
  /** Terminal height in rows. */
  rows: number;
  /** Lines the input editor currently occupies. */
  editorLines: number;
  /** Tool cards available to show. */
  toolCards: number;
  /** Queued items available to show. */
  todos: number;
  /** Whether the home screen would otherwise be drawn. */
  wantHero: boolean;
}

export interface ViewportPlan {
  toolCards: number;
  todoRows: number;
  liveLines: number;
  showHero: boolean;
}

/** Rows the input frame, status line and key hints always need. */
const CHROME_ROWS = 6;
/** Rows the home screen occupies when it is drawn. */
const HERO_ROWS = 12;
/** Streaming never drops below this, or a running turn looks like a hang. */
const LIVE_MIN = 2;
const MAX_TOOL_CARDS = 5;
const MAX_TODO_ROWS = 6;

/**
 * Decide what fits. Order of sacrifice, least useful first: the home screen
 * goes before tool cards, tool cards before the queue, and streaming text
 * keeps a floor because a turn with nothing visible reads as a hang.
 */
export function planViewport(input: ViewportInput): ViewportPlan {
  const rows = Math.max(8, Math.floor(input.rows));
  const editor = Math.max(1, Math.floor(input.editorLines));

  let free = rows - CHROME_ROWS - editor;

  // The home screen only appears before the first turn, and only when there
  // is genuinely room: half a home screen is worse than none.
  const showHero = input.wantHero && free >= HERO_ROWS + LIVE_MIN;
  if (showHero) free -= HERO_ROWS;

  // The bounded sections are allocated first and streaming absorbs the rest.
  // Taking a share of the remainder for streaming up front looks fair and is
  // not: on a tall terminal it starved the queue of rows there was plenty of
  // room for.
  let budget = free - LIVE_MIN;
  const toolCards = clamp(input.toolCards, 0, Math.max(0, Math.min(MAX_TOOL_CARDS, budget)));
  budget -= toolCards;
  const todoRows = clamp(input.todos, 0, Math.max(0, Math.min(MAX_TODO_ROWS, budget)));
  budget -= todoRows;

  // Whatever is left goes to the running turn, never below the floor — a turn
  // with nothing visible reads as a hang.
  const liveLines = LIVE_MIN + Math.max(0, budget);

  return { toolCards, todoRows, liveLines, showHero };
}

/**
 * The smallest frame this can produce: the input and the chrome around it,
 * plus the streaming floor. Below this the terminal is simply too short and
 * nothing can be given up to fix it.
 */
export function minimumHeight(editorLines: number): number {
  return CHROME_ROWS + Math.max(1, Math.floor(editorLines)) + LIVE_MIN;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, Math.floor(value)));
}
