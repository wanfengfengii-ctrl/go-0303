package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"shieldtunnel/domain"
)

// Summarize computes the content-addressed RuleSummary for a snapshot. The
// summary is a SHA-256 digest over the canonical JSON form of every field
// except the summary itself, so a lock request can prove its rules were fresh
// at lock time and any catalogue change invalidates prior summaries.
func Summarize(snap domain.RuleSnapshot) domain.RuleSummary {
	snap.Summary = ""
	b, err := json.Marshal(snap)
	if err != nil {
		// json.Marshal cannot fail on this value tree; guard defensively.
		return domain.RuleSummary("")
	}
	sum := sha256.Sum256(b)
	return domain.RuleSummary(hex.EncodeToString(sum[:]))
}
