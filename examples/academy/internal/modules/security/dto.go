package security

import (
	"context"
	"net/mail"
	"time"

	"github.com/dygoframework/dygo/validate"
)

// Credentials is the body of register and login.
type Credentials struct {
	Email    string `json:"email" example:"ada@example.com"`
	Password string `json:"password" example:"correct horse battery"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *Credentials) Validate(context.Context) error {
	var v validate.Errors
	if a, err := mail.ParseAddress(b.Email); err != nil || a.Address != b.Email {
		v.Add("body", "email", "must be an email address")
	}
	if len(b.Password) < 8 {
		v.Add("body", "password", "must be at least 8 characters")
	}
	return v.Err()
}

// TokenOut is the response of login and refresh. The access token is sent
// as "Authorization: Bearer ..." until ExpiresAt; then the refresh token
// buys a new pair at POST /auth/refresh.
type TokenOut struct {
	AccessToken  string    `json:"accessToken"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	RefreshToken string    `json:"refreshToken"`
}

// RefreshBody is the body of refresh and logout.
type RefreshBody struct {
	RefreshToken string `json:"refreshToken"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *RefreshBody) Validate(context.Context) error {
	var v validate.Errors
	if b.RefreshToken == "" {
		v.Add("body", "refreshToken", "is required")
	}
	return v.Err()
}
