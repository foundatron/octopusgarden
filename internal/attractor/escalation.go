package attractor

import "log/slog"

// modelTier represents a model quality tier used by escalation state.
// tierFrugal is the starting tier; tierPrimary is escalated to after consecutive failures.
type modelTier int

const (
	// tierFrugal is the initial cost-efficient tier (--frugal-model).
	tierFrugal modelTier = 1
	// tierPrimary is the escalated tier (--model).
	tierPrimary modelTier = 2
)

// String returns a human-readable label for the tier, used in log attributes.
func (t modelTier) String() string {
	switch t {
	case tierFrugal:
		return "frugal"
	case tierPrimary:
		return "primary"
	default:
		return "unknown"
	}
}

const (
	// escalateAfterFailures is the number of consecutive non-improving iterations
	// required to escalate from tierFrugal to tierPrimary.
	// Note: with a default stallLimit=3, escalation fires after 2 failures and the run
	// terminates after 3. Escalation therefore buys exactly one extra attempt at the
	// higher tier — this is intentional and acceptable.
	escalateAfterFailures = 2
	// downgradeAfterImprovements is the number of consecutive improving iterations
	// required to downgrade from tierPrimary back to tierFrugal.
	downgradeAfterImprovements = 5
)

// escalationState manages automatic model tier escalation and downgrade.
// It tracks consecutive failures and improvements to decide when to switch tiers.
// Use newEscalationState to construct; a nil pointer means escalation is disabled.
type escalationState struct {
	currentTier         modelTier
	consecutiveFailures int
	consecutiveImprove  int
	models              [2]string // index 0 = tierFrugal, index 1 = tierPrimary
}

// newEscalationState returns an escalationState starting at tierFrugal.
// Returns nil when frugalModel is empty (escalation disabled).
func newEscalationState(frugalModel, primaryModel string, logger *slog.Logger) *escalationState {
	if frugalModel == "" {
		return nil
	}
	if frugalModel == primaryModel {
		logger.Debug("frugal-model equals primary model; escalation machinery will run but model never changes", "model", frugalModel)
	}
	return &escalationState{
		currentTier: tierFrugal,
		models:      [2]string{frugalModel, primaryModel},
	}
}

// currentModel returns the model string for the current tier.
func (e *escalationState) currentModel() string {
	return e.models[e.currentTier-1]
}

// recordOutcome updates escalation state based on whether the last iteration improved.
// "Improved" means the iteration was validated and satisfaction strictly exceeded the
// previous best — the caller (processValidation via runState) determines this.
// Escalation fires after escalateAfterFailures consecutive non-improved outcomes.
// Downgrade fires after downgradeAfterImprovements consecutive improved outcomes.
func (e *escalationState) recordOutcome(improved bool, logger *slog.Logger) {
	if improved {
		e.consecutiveImprove++
		e.consecutiveFailures = 0
		if e.consecutiveImprove >= downgradeAfterImprovements && e.currentTier > tierFrugal {
			from := e.currentTier
			e.currentTier = tierFrugal
			e.consecutiveImprove = 0
			logger.Info("model downgraded",
				"from_tier", from,
				"to_tier", tierFrugal,
				"model", e.currentModel(),
				"reason", "consecutive_improvements",
			)
		}
	} else {
		e.consecutiveFailures++
		e.consecutiveImprove = 0
		if e.consecutiveFailures >= escalateAfterFailures && e.currentTier < tierPrimary {
			from := e.currentTier
			e.currentTier = tierPrimary
			e.consecutiveFailures = 0
			logger.Info("model escalated",
				"from_tier", from,
				"to_tier", tierPrimary,
				"model", e.currentModel(),
				"reason", "consecutive_failures",
			)
		}
	}
}
