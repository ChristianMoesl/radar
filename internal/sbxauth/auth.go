package sbxauth

import "strings"

// IsRequired reports whether SBX output indicates that the Docker session must
// be refreshed before retrying the command.
func IsRequired(detail string) bool {
	detail = strings.ToLower(detail)
	for _, marker := range []string{
		"401 unauthorized",
		"not authenticated to docker",
		"no valid user session found",
		"secret not found",
		"sign-in required",
		"sign in required",
		"please sign in to docker",
		"not signed in",
		"run sbx login",
		"run 'sbx login'",
		`run "sbx login"`,
	} {
		if strings.Contains(detail, marker) {
			return true
		}
	}
	return false
}
