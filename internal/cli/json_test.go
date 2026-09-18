package cli

import "encoding/json"

func decode(raw []byte, v any) error { return json.Unmarshal(raw, v) }
