package model

// Order represents a sort order
type Order struct {
	Field     string `json:"field"`
	Direction string `json:"direction"` // "asc" or "desc"
}
