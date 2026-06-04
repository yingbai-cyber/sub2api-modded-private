package service

// maxEmptyStreamRetries is the number of times we retry when the upstream
// returns HTTP 200 but the SSE stream contains zero meaningful events.
// Only applies to buffered (non-client-streaming) paths where we haven't
// written any HTTP response to the client yet.
const maxEmptyStreamRetries = 1

// isEmptyStreamResult returns true when a ForwardResult indicates the upstream
// delivered an empty SSE stream (connected successfully but sent no tokens).
func isEmptyStreamResult(r *ForwardResult) bool {
	if r == nil {
		return true
	}
	return r.Usage.InputTokens == 0 && r.Usage.OutputTokens == 0
}
