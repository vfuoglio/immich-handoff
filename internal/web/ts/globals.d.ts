// What the page's scripts rely on that is not part of the browser's own types.

/** View Transitions are not in every browser's types yet. */
interface Document {
  startViewTransition?(update: () => void): unknown;
}

/** htmx is loaded by its own script tag (static/htmx.min.js) and exposes a global. */
declare const htmx: {
  trigger(element: Element, eventName: string): void;
};

/** The parts of htmx's `htmx:beforeSwap` event that the page reads and changes. */
interface HtmxBeforeSwapDetail {
  xhr: XMLHttpRequest;
  shouldSwap: boolean;
  isError: boolean;
}

/** The parts of htmx's `htmx:afterSwap` event that the page reads. */
interface HtmxAfterSwapDetail {
  elt: Element;
}

/** The parts of htmx's `htmx:confirm` event the page uses to ask in its own dialog. */
interface HtmxConfirmDetail {
  elt: Element;
  question: string | null;
  issueRequest(skipConfirmation?: boolean): void;
}

interface DocumentEventMap {
  /** Re-sent by app.ts for every event the server streams (see live.ts); the live scene and the numbers listen to it. */
  "handoff:live": CustomEvent<import("./live").LiveEvent>;
  /** Re-sent by app.ts when the live stream goes down or comes back. */
  "handoff:stream": CustomEvent<{ up: boolean }>;
  "htmx:confirm": CustomEvent<HtmxConfirmDetail>;
  "htmx:beforeSwap": CustomEvent<HtmxBeforeSwapDetail>;
  "htmx:afterSwap": CustomEvent<HtmxAfterSwapDetail>;
  "htmx:oobAfterSwap": CustomEvent<unknown>;
  /** Sent by the server (HX-Trigger) after Tags were switched on. */
  "tags-on": CustomEvent<unknown>;
}
