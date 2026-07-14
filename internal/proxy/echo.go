// The built-in echo upstream: a deterministic origin server so a scenario
// can be exercised end to end with nothing but the slowlane binary — no
// real backend, no second tool. `slowlane echo` serves this handler.
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// echoReply is the JSON body the echo upstream returns. Header values are
// flattened to their first occurrence; encoding/json sorts map keys, so
// the body is byte-stable for a given request.
type echoReply struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Query    string            `json:"query,omitempty"`
	Headers  map[string]string `json:"headers"`
	BodySize int64             `json:"body_size"`
}

// EchoHandler returns the echo upstream handler. It answers every request
// with 200 and a JSON description of what it received, plus an
// X-Slowlane-Echo marker so tests can assert the response really crossed
// the upstream hop (an injected fault never carries it).
func EchoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, _ := io.Copy(io.Discard, r.Body)
		reply := echoReply{
			Method:   r.Method,
			Path:     r.URL.Path,
			Query:    r.URL.RawQuery,
			Headers:  map[string]string{},
			BodySize: size,
		}
		for name := range r.Header {
			reply.Headers[name] = r.Header.Get(name)
		}
		body, _ := json.MarshalIndent(reply, "", "  ")
		body = append(body, '\n')
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("X-Slowlane-Echo", "1")
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})
}
