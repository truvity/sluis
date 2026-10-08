package edgecloudflare

import "encoding/json"

func jsonDocument(st []map[string]any) (string, error) {
	raw, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": st})
	return string(raw), err
}
