// The worker's limits, in one place. They match the exit's own
// (`http_proxy/limits.go`), so a request the exit would refuse for its size
// is refused here, before it costs a mixnet round trip.

/** Largest request body the worker sends, in bytes (the exit's `MaxRequestBodyBytes`). */
export const MAX_REQUEST_BODY_BYTES = 128 * 1024;

/** Longest provider URL the worker sends, in characters (the exit's `MaxURLLength`). */
export const MAX_URL_LENGTH = 2 * 1024;
