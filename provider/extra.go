package provider

import "encoding/json"

// marshalWithExtra serialises req to JSON, then merges the extra keys into
// the top-level object.  Keys in extra override struct-generated keys.
// If extra is nil or empty, it behaves identically to json.Marshal(req).
func marshalWithExtra(req any, extra map[string]any) ([]byte, error) {
	base, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return base, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range extra {
		merged[k] = v
	}
	return json.Marshal(merged)
}
