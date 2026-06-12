package httpc

import "errors"

// ErrResponseTooLarge is returned (wrapped) when a response body exceeds the
// configured maximum. See [WithMaxResponseBytes]. Detect it with [errors.Is].
var ErrResponseTooLarge = errors.New("httpc: response body exceeds limit")

// ErrEmptyBody is returned by the JSON convenience methods when the server sends
// an empty (or whitespace-only) response body but a decode target was provided.
// It lets callers distinguish "no content" (e.g. 204, an empty 502) from
// malformed JSON. Detect it with [errors.Is]. Use the Raw methods if an empty
// body is expected and should not be treated as an error.
var ErrEmptyBody = errors.New("httpc: empty response body")
