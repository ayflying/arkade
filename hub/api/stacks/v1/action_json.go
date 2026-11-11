package v1

import "encoding/json"

func (r ActionRes) MarshalJSON() ([]byte, error) {
	if r.Backup != nil {
		return json.Marshal(r.Backup)
	}
	return json.Marshal(r.Operation)
}
