package donations

import "time"

// Goal — полоса сбора донатной цели.
type Goal struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	Title        string    `json:"title"`
	Currency     string    `json:"currency"`
	StartAmount  float64   `json:"startAmount"`
	RaisedAmount float64   `json:"raisedAmount"`
	GoalAmount   float64   `json:"goalAmount"`
	Active       bool      `json:"active"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
