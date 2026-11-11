// Package v1 declares the native GoFrame user administration API.
package v1

import "encoding/json"

// UserView deliberately has no password or password hash field.
type UserView struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Disabled    bool     `json:"disabled"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	GroupIDs    []string `json:"group_ids"`
}

// Optional distinguishes omitted PATCH fields from explicit null (the zero value).
// A pointer alone cannot preserve that existing JSON contract.
type Optional[T any] struct {
	Value T
	Set   bool
}

// UnmarshalValue supports GoFrame's scalar struct converter. GF skips nil map
// values entirely, so explicit null still needs the strict decoded request.
func (o *Optional[T]) UnmarshalValue(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return o.UnmarshalJSON(data)
}

func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		var zero T
		o.Value = zero
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = value
	return nil
}
