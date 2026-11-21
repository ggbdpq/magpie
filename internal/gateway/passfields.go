package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
)

// jsonTopKeys is the set of top-level keys a request struct's json tags
// consume: what a cross-protocol build of that entry knows how to map.
func jsonTopKeys(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" {
			out[name] = true
		}
	}
	return out
}

var (
	anthropicTop = jsonTopKeys(reflect.TypeFor[aRequest]())
	chatTop      = jsonTopKeys(reflect.TypeFor[cRequest]())
	responsesTop = jsonTopKeys(reflect.TypeFor[rRequest]())
)

// passFields picks a provider's allowlisted unknown fields out of the
// request body: names matched exactly, a key the entry parser consumes
// is never taken (the build maps its own schema), and what is left goes
// as the client sent it (imxzt, #7376). Nothing allowlisted in the body
// is nil.
func passFields(body []byte, known map[string]bool, allow []string) map[string]any {
	if len(allow) == 0 || len(body) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	var out map[string]any
	for _, k := range allow {
		if k == "" || known[k] {
			continue
		}
		raw, ok := m[k]
		if !ok {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	return out
}

// withPassFields merges a request's passlisted fields into a translated
// body before it is marshalled: what the build set stays (its key is
// taken), what the entry parser didn't know rides as it came.
func withPassFields(out map[string]any, r *Request) {
	for k, v := range r.passthrough {
		if _, taken := out[k]; !taken {
			out[k] = v
		}
	}
}
