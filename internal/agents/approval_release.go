//go:build release

package agents

// approvalAlwaysRequired makes the human-approval gate on command execution
// unwaivable (production audit I6). Release builds carry the `release` build
// tag (see .goreleaser.yaml), so in a shipped binary neither a zero-valued
// RequireApproval field nor an explicit waiver can run a command without a
// human decision — the only path to execution is through the approval flow,
// which sets the unexported `approved` field the gate honours.
//
// The audit's reasoning: while the tool sandbox lacks real isolation (M5),
// the approval gate is the primary containment for arbitrary command
// execution, and a control that important should not be one forgotten struct
// field away from off.
const approvalAlwaysRequired = true
