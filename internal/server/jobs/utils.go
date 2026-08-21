package jobs

import "regexp"

// Redact replaces sensitive patterns in a string with placeholders:
//   - URI userinfo (scheme://user:pass@ → scheme://***@)
//   - password= values (password=secret → password=***)
//   - temporary secret file paths (*.bmc-secret* → ***)
func Redact(s string) string {
	s = uriUserinfoRE.ReplaceAllString(s, "$1://***@")
	s = passwordRE.ReplaceAllString(s, "${1}password=***")
	s = bmcSecretFileRE.ReplaceAllString(s, "***")
	return s
}

var (
	uriUserinfoRE  = regexp.MustCompile(`(?i)((?:https?|postgresql|postgres|mysql|mongodb|sqlite|restic|rclone))://[^/@:]+:[^/@]+@`)
	passwordRE     = regexp.MustCompile(`(?i)([?&])password=[^\s&"']+`)
	bmcSecretFileRE = regexp.MustCompile(`[^\s"']*\.bmc-secret[^\s"']*`)
)