package onboarding

import (
	"os"
	"strings"

	"radar/internal/config"
	"radar/internal/setup"
)

// Never prefill a text input with a secret, even a masked one. Empty input keeps
// an existing credential; only explicit replacements become disk updates.
func (w wizard) secret(title, hint, environment, namespace, key string, updates config.Secrets) (string, error) {
	if value := strings.TrimSpace(os.Getenv(environment)); value != "" {
		w.ui.Print("%s: using %s (not copied to disk). Unset it to replace the stored credential through setup.\n", title, environment)
		return value, nil
	}
	saved, err := config.LoadSecrets()
	if err != nil {
		return "", err
	}
	existing := saved[namespace][key]
	hint += " Input is hidden."
	check := setup.Required
	if existing != "" {
		hint += " Leave blank to keep the stored credential."
		check = nil
	}
	value, err := w.ui.Input(setup.Question{Title: title, Hint: hint, Secret: true, Validate: check})
	if err != nil {
		return "", err
	}
	if value == "" {
		return existing, nil
	}
	if updates[namespace] == nil {
		updates[namespace] = map[string]string{}
	}
	updates[namespace][key] = value
	return value, nil
}
