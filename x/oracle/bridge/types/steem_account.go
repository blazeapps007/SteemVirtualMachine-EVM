package types

import "regexp"

// steemAccountNameRegex enforces Steem's account name rules: 3-16 characters,
// lowercase letters and digits, with dot- or dash-separated segments (each
// segment starting with a letter), e.g. "alice", "steem.gateway", "foo-bar".
var steemAccountNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*$`)

// ValidateSteemAccountName validates a Steem blockchain account name.
func ValidateSteemAccountName(name string) error {
	if len(name) < 3 || len(name) > 16 {
		return ErrInvalidSteemAccount.Wrapf("account name %q must be 3-16 characters", name)
	}
	if !steemAccountNameRegex.MatchString(name) {
		return ErrInvalidSteemAccount.Wrapf("account name %q is not a valid Steem account name", name)
	}
	return nil
}
