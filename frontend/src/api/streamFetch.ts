// streamFetch is the fetch the generated SSE client uses for long-lived
// streams. The client always hands fetch a Request object and reads the body
// through pipeThrough(TextDecoderStream). In WebKit, the desktop app's
// webview, aborting such a stream can leave its HTTP/1.1 connection open and
// still reading. WebKit allows six connections per host, so after a few
// leaked streams every later request to the gateway (polls, sends, terminal
// tickets) waits forever. Calling fetch with a URL and the caller's own
// signal lets WebKit close the connection when the stream is aborted.
export function streamFetch(signal: AbortSignal): typeof fetch {
  return (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    return fetch(request.url, {
      method: request.method,
      headers: request.headers,
      credentials: request.credentials,
      cache: request.cache,
      redirect: request.redirect,
      referrerPolicy: request.referrerPolicy,
      signal,
    });
  };
}
