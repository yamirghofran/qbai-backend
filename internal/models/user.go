package models

import "github.com/google/uuid"

// UserProfile stores information about the authenticated user.
type UserProfile struct {
	DatabaseID    uuid.UUID `json:"-"`  // Our internal DB UUID (omit from JSON response to client)
	GoogleID      string    `json:"id"` // Google's ID (keep as 'id' in JSON)
	Email         string    `json:"email"`
	VerifiedEmail bool      `json:"verified_email"`
	Name          string    `json:"name"`
	GivenName     string    `json:"given_name"`
	FamilyName    string    `json:"family_name"`
	Picture       string    `json:"picture"`
	Locale        string    `json:"locale"`
}
