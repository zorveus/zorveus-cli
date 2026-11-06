package api

import (
	"context"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type MeUser struct {
	ID        string  `json:"user_id"`
	Email     string  `json:"email"`
	FirstName string  `json:"first_name"`
	LastName  *string `json:"last_name"`
}

type MeSession struct {
	Authenticated bool      `json:"authenticated"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type Workspace struct {
	ID   string `json:"org_id"`
	Name string `json:"org_name"`
	Role string `json:"role"`
}

type MeResponse struct {
	User    MeUser      `json:"user"`
	Session MeSession   `json:"session"`
	Orgs    []Workspace `json:"orgs"`
}

func (client *Client) GetMe(ctx context.Context, origin, organization, token string) (MeResponse, error) {
	var result MeResponse
	if err := client.readResource(ctx, origin, "/me", token, &result); err != nil {
		return MeResponse{}, err
	}
	if !result.Session.Authenticated {
		return MeResponse{}, problem.New("login_required", "The API did not confirm an authenticated session. Run zorveus login.", 3)
	}
	if result.User.ID == "" || result.User.Email == "" || result.Session.ExpiresAt.IsZero() {
		return MeResponse{}, problem.New("invalid_response", "The API returned incomplete user or session details.", 1)
	}
	for _, workspace := range result.Orgs {
		if workspace.ID != organization {
			return MeResponse{}, unexpectedResource()
		}
	}
	return result, nil
}
