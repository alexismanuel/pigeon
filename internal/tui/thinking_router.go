package tui

import "strings"

// xmlThinkingRouter splits a token stream that embeds <thinking>…</thinking>
// XML blocks into separate thinking-token and regular-token callbacks.
//
// Some models (e.g. DeepSeek-R1, QwQ) signal their chain-of-thought with these
// XML tags inside the normal content stream instead of via a dedicated reasoning
// field. The router transparently intercepts those tokens so they are rendered
// in the collapsible thinking block rather than in the assistant message body.
type xmlThinkingRouter struct {
	inThinking bool
	buf        string // lookahead buffer for partial tag detection
	onToken    func(string)
	onThinking func(string)
}

const thinkOpen  = "<thinking>"
const thinkClose = "</thinking>"

func newXMLThinkingRouter(onToken, onThinking func(string)) *xmlThinkingRouter {
	return &xmlThinkingRouter{onToken: onToken, onThinking: onThinking}
}

// feed processes an incoming token, routing content to the appropriate callback.
func (r *xmlThinkingRouter) feed(token string) {
	r.buf += token
	for {
		if !r.inThinking {
			if idx := strings.Index(r.buf, thinkOpen); idx >= 0 {
				// Emit any text before the opening tag as a regular token.
				if idx > 0 {
					r.onToken(r.buf[:idx])
				}
				r.buf = r.buf[idx+len(thinkOpen):]
				r.inThinking = true
				continue
			}
			// Hold back any suffix that could be the start of the opening tag.
			if partial := xmlPartialSuffix(r.buf, thinkOpen); partial > 0 {
				safe := r.buf[:len(r.buf)-partial]
				if safe != "" {
					r.onToken(safe)
				}
				r.buf = r.buf[len(r.buf)-partial:]
				return
			}
			if r.buf != "" {
				r.onToken(r.buf)
				r.buf = ""
			}
			return
		} else {
			if idx := strings.Index(r.buf, thinkClose); idx >= 0 {
				// Emit buffered thinking content before the closing tag.
				if idx > 0 {
					r.onThinking(r.buf[:idx])
				}
				r.buf = r.buf[idx+len(thinkClose):]
				r.inThinking = false
				continue
			}
			// Hold back any suffix that could be the start of the closing tag.
			if partial := xmlPartialSuffix(r.buf, thinkClose); partial > 0 {
				safe := r.buf[:len(r.buf)-partial]
				if safe != "" {
					r.onThinking(safe)
				}
				r.buf = r.buf[len(r.buf)-partial:]
				return
			}
			if r.buf != "" {
				r.onThinking(r.buf)
				r.buf = ""
			}
			return
		}
	}
}

// xmlPartialSuffix returns the length of the longest suffix of s that is also
// a proper prefix of tag (i.e. a partial tag at the end of the buffer that
// could complete into tag with future tokens).
func xmlPartialSuffix(s, tag string) int {
	maxCheck := len(tag) - 1
	if maxCheck > len(s) {
		maxCheck = len(s)
	}
	for length := maxCheck; length > 0; length-- {
		if strings.HasSuffix(s, tag[:length]) {
			return length
		}
	}
	return 0
}
