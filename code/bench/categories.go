package bench

// Failure categories stored in bench_attempts.failure_category.
const (
	CategoryResolved      = "resolved"
	CategoryIncompleteFix = "incomplete_fix"
	CategoryRegression    = "regression"
	CategoryEmptyPatch    = "empty_patch"
	CategoryApplyFailed   = "patch_apply_failed"
	CategoryAgentTimeout  = "agent_timeout"
	CategoryAgentError    = "agent_error"
	CategoryModelError    = "model_error"
	CategoryBudget        = "budget_exceeded"
	// CategoryInfraError covers image, container and verifier failures; such
	// attempts are retried and excluded from scores.
	CategoryInfraError = "infra_error"
)

// Classify picks the category of a graded attempt. agentCategory is the
// agent phase outcome ("" when the agent finished normally); when the agent
// stopped early but its partial patch still resolves the task, the attempt
// counts as resolved.
func Classify(agentCategory string, g GradeResult) string {
	switch {
	case g.Resolved:
		return CategoryResolved
	case agentCategory != "":
		return agentCategory
	case g.EmptyPatch:
		return CategoryEmptyPatch
	case g.ApplyFailed:
		return CategoryApplyFailed
	case g.F2PPassed < g.F2PTotal:
		return CategoryIncompleteFix
	case g.P2PPassed < g.P2PTotal:
		return CategoryRegression
	default:
		// All listed tests passed but the verifier reported failure (for
		// example the test patch did not apply or the parser failed).
		return CategoryIncompleteFix
	}
}
