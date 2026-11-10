package provision

import (
	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func unknownServiceRotation() error {
	return problem.New("rotation_outcome_unknown", "A service-key rotation was attempted but its result was not confirmed. Inspect the saved journal and resource before explicitly recovering it. The CLI will not rotate it again automatically.", 5)
}

func (run *execution) recoverServiceCredential(index int, step Step, account string) error {
	progress := &run.journal.Steps[index]
	if progress.RotationStarted && !progress.RotationReceived {
		return unknownServiceRotation()
	}
	current, err := run.remote.serviceKey(progress.ID)
	if err != nil {
		return err
	}
	if !serviceKeyActive(current, run.remote.service.Auth.Now()) {
		return conflict()
	}
	if progress.SecretPrefix != "" && current.TokenPrefix != progress.SecretPrefix {
		return conflict()
	}
	var replacement api.ServiceKeyCredential
	err = run.remote.call(stepScopes(step), func(client *api.Client, login auth.Login) error {
		// Authenticate first, then record intent immediately before sending rotation.
		progress.RotationStarted, progress.RotationReceived = true, false
		if err := run.save(); err != nil {
			return err
		}
		var err error
		replacement, err = client.RotateServiceKey(run.remote.ctx, login.APIOrigin, login.Organization, progress.ID, login.AccessToken)
		return err
	})
	if err != nil {
		code := problem.Safe(err).Code
		if code == "login_required" || code == "authorization_required" || code == "access_denied" {
			progress.RotationStarted = false
			if saveErr := run.save(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	progress.RotationReceived = true
	progress.SecretPrefix = replacement.Key.TokenPrefix
	if err := run.save(); err != nil {
		return err
	}
	credential := Credential{ResourceID: progress.ID, CredentialID: progress.CredentialID, Secret: replacement.Secret}
	if err := run.secrets.Save(account, credential); err != nil {
		return err
	}
	actual, err := run.remote.serviceKey(progress.ID)
	if err != nil {
		return err
	}
	if actual.TokenPrefix != progress.SecretPrefix || !sameFields(project(fields(actual), step.After), step.After) {
		return conflict()
	}
	progress.Done = true
	return run.save()
}
