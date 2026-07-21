//go:build !release

package agents

// approvalAlwaysRequired is false in development and test builds so tests can
// exercise command execution without a human in the loop. Release builds set
// it to true via the `release` build tag — see approval_release.go for the
// rationale (production audit I6).
const approvalAlwaysRequired = false
