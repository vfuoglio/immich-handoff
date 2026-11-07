// What the page's scripts rely on that is not part of the browser's own types.

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
  "htmx:confirm": CustomEvent<HtmxConfirmDetail>;
  "htmx:beforeSwap": CustomEvent<HtmxBeforeSwapDetail>;
  "htmx:afterSwap": CustomEvent<HtmxAfterSwapDetail>;
  /** Sent by the server (HX-Trigger) after Tags were switched on. */
  "tags-on": CustomEvent<unknown>;
}
