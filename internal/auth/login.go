package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type LoginOptions struct {
	APIOrigin       string
	DashboardOrigin string
	Scope           string
	OpenBrowser     func(context.Context, string)
}

type callbackResult struct {
	code string
	err  error
}

func (service *Service) Login(ctx context.Context, options LoginOptions) (LoginSummary, error) {
	if err := service.checkCredentialStore(ctx); err != nil {
		return LoginSummary{}, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return LoginSummary{}, problem.New("callback_unavailable", "Could not open the loopback callback listener.", 1)
	}
	callbackURL := "http://" + listener.Addr().String() + "/callback"
	state := oauth2.GenerateVerifier()
	verifier := oauth2.GenerateVerifier()
	result := make(chan callbackResult, 1)
	server := &http.Server{
		Handler:           callbackHandler(listener.Addr().String(), state, result),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		MaxHeaderBytes:    8192,
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			_ = server.Close()
		}
	}()
	go func() { _ = server.Serve(listener) }()
	configuration := oauth2.Config{
		ClientID:    api.ClientID,
		RedirectURL: callbackURL,
		Endpoint:    oauth2.Endpoint{AuthURL: options.DashboardOrigin + "/oauth/authorize"},
	}
	authorizationURL := configuration.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("credential_type", "management"),
		oauth2.SetAuthURLParam("scope", options.Scope),
		oauth2.SetAuthURLParam("renewal_allowed", "true"),
	)
	options.OpenBrowser(ctx, authorizationURL)
	select {
	case <-ctx.Done():
		return LoginSummary{}, problem.New("login_canceled", "Login was canceled or timed out. The saved login was not changed.", 3)
	case callback := <-result:
		if callback.err != nil {
			return LoginSummary{}, callback.err
		}
		requestedAt := service.Now()
		tokens, err := service.API.ExchangeCode(ctx, options.APIOrigin, callback.code, callbackURL, verifier)
		if err != nil {
			return LoginSummary{}, err
		}
		login := loginFromTokens(options.APIOrigin, tokens, requestedAt)
		if !scopesAreSubset(tokens.Scope, options.Scope) {
			return LoginSummary{}, service.discardLogin(login, "The API returned permissions that were not requested.")
		}
		return service.replaceLogin(ctx, login)
	}
}

func (service *Service) checkCredentialStore(ctx context.Context) error {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = service.Store.Load()
	if errors.Is(err, ErrNoLogin) {
		return nil
	}
	return err
}

func (service *Service) replaceLogin(ctx context.Context, replacement Login) (LoginSummary, error) {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return LoginSummary{}, service.discardLogin(replacement, "Could not lock the saved login for replacement.")
	}
	defer unlock()
	previous, err := service.loadLogin("")
	if err != nil && !errors.Is(err, ErrNoLogin) {
		return LoginSummary{}, service.discardLogin(replacement, "Could not read the previous login. It was not replaced.")
	}
	if err == nil {
		if err := service.revokeLogin(ctx, previous); err != nil {
			return LoginSummary{}, service.discardLogin(replacement, "Could not confirm revocation of the previous login. Its saved credentials were retained. Retry login or revoke the session from the dashboard.")
		}
	}
	if err := service.Store.Save(replacement); err != nil {
		return LoginSummary{}, service.discardLogin(replacement, "Could not save the new login. Any previous login has already been revoked. Unlock the credential store and log in again.")
	}
	return LoginSummary{
		APIOrigin:                replacement.APIOrigin,
		Organization:             replacement.Organization,
		Scope:                    replacement.Scope,
		ExpiresAt:                replacement.ExpiresAt,
		Renewal:                  replacement.RefreshToken != "",
		PreviousSessionMayRemain: previous.RefreshBlocked,
	}, nil
}

func (service *Service) discardLogin(login Login, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := service.revokeLogin(ctx, login); err != nil {
		return problem.New("login_incomplete", message+" The new session also could not be revoked. Revoke it from the dashboard.", 1)
	}
	return problem.New("login_incomplete", message+" The new session was revoked.", 1)
}

func callbackHandler(expectedHost, expectedState string, result chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if request.Method != http.MethodGet || request.URL.Path != "/callback" || request.Host != expectedHost {
			http.Error(writer, "Invalid callback.", http.StatusBadRequest)
			return
		}
		query := request.URL.Query()
		states := query["state"]
		if len(states) != 1 || subtle.ConstantTimeCompare([]byte(states[0]), []byte(expectedState)) != 1 {
			http.Error(writer, "Invalid callback state.", http.StatusBadRequest)
			return
		}
		codes := query["code"]
		failures := query["error"]
		if len(codes)+len(failures) != 1 || (len(codes) == 1 && codes[0] == "") || (len(failures) == 1 && failures[0] == "") {
			http.Error(writer, "Invalid callback parameters.", http.StatusBadRequest)
			return
		}
		callback := callbackResult{}
		if len(failures) == 1 {
			callback.err = problem.New("authorization_denied", "Browser authorization was denied. The saved login was not changed.", 3)
		} else {
			callback.code = codes[0]
		}
		select {
		case result <- callback:
			_, _ = io.WriteString(writer, "Authorization received. Return to the terminal. You can close this tab.")
		default:
			http.Error(writer, "This callback has already been received.", http.StatusConflict)
		}
	})
}
