package council

// needCritique reports whether an ask turn's reviewer reads the members' reports (spec 7.5, D17), and
// why: the Thorough mode, an unattended turn, a tool result any role saw only part of (the lead's own
// included), a member that did not finish, a report whose confidence is low, any Coder report, even one
// whose task never started (it holds the bot key: its changes need not leave a file in its work dir,
// D20), or a member's proposal. A tool result that is an error does not count by itself. When
// none holds, the reviewer is not needed. A report's open questions and findings without evidence do
// not count: a non-data role's findings carry no evidence by design, and the reviewer checks both when
// it runs.
func needCritique(thorough, unattended, truncated bool, reports []memberResult) (bool, string) {
	switch {
	case thorough:
		return true, "thorough"
	case unattended:
		return true, "unattended"
	case truncated:
		return true, "truncated"
	}
	for _, m := range reports {
		if m.report != nil && m.report.partial {
			return true, "incomplete"
		}
	}
	for _, m := range reports {
		if m.report != nil && m.report.Confidence == confidenceLow {
			return true, "low confidence"
		}
	}
	for _, m := range reports {
		if m.coder != nil {
			return true, "coder"
		}
	}
	for _, m := range reports {
		if m.report != nil && len(m.report.Proposals) > 0 {
			return true, "proposal"
		}
	}
	return false, "not needed"
}
