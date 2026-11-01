/**
 * Incremental parser for a text/event-stream body (WHATWG "server-sent events" rules):
 * lines end in LF, CRLF or CR; "field:value" with one optional space after the colon; lines starting
 * with ":" are comments (keep-alives); an empty line dispatches the event; a missing event name is
 * "message". Kept free of imports so it can be unit tested with plain node.
 */
export type SSEMessage = { event: string; data: string; id?: string };

export function createSSEParser(onMessage: (m: SSEMessage) => void) {
  let buffer = "";
  let event = "";
  let data: string[] = [];
  let id: string | undefined;
  // A chunk ending in "\r" may be the first half of "\r\n".
  let pendingCR = false;

  const dispatch = () => {
    if (data.length) onMessage({ event: event || "message", data: data.join("\n"), id });
    event = "";
    data = [];
  };

  const line = (l: string) => {
    if (l === "") return dispatch();
    if (l.startsWith(":")) return;
    const colon = l.indexOf(":");
    const field = colon < 0 ? l : l.slice(0, colon);
    let value = colon < 0 ? "" : l.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") event = value;
    else if (field === "data") data.push(value);
    else if (field === "id" && !value.includes("\0")) id = value;
  };

  return {
    push(chunk: string) {
      if (pendingCR && chunk.startsWith("\n")) chunk = chunk.slice(1);
      pendingCR = false;
      buffer += chunk;
      let start = 0;
      for (let i = 0; i < buffer.length; i++) {
        const c = buffer[i];
        if (c !== "\n" && c !== "\r") continue;
        line(buffer.slice(start, i));
        if (c === "\r") {
          if (i + 1 === buffer.length) pendingCR = true;
          else if (buffer[i + 1] === "\n") i++;
        }
        start = i + 1;
      }
      buffer = buffer.slice(start);
    },
    /** End of stream: a final unterminated line still counts, then the event is dispatched. */
    end() {
      if (buffer) line(buffer);
      buffer = "";
      dispatch();
    },
  };
}
