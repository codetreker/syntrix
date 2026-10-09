package model

// Query represents a database query
type Query struct {
	Collection  string  `json:"collection"`
	Filters     Filters `json:"filters"`
	OrderBy     []Order `json:"orderBy"`
	Limit       int     `json:"limit"`
	StartAfter  string  `json:"startAfter"` // Opaque continuation returned by QueryPage.NextCursor.
	ShowDeleted bool    `json:"showDeleted"`
}

// QueryPage is one ordered page of source-validated documents. A non-nil cursor
// continues candidate traversal; it does not promise another matching document.
type QueryPage struct {
	Documents      []Document `json:"documents"`
	NextCursor     *string    `json:"nextCursor"`
	EffectiveOrder []Order    `json:"effectiveOrder"`
}
