package provider

// Google refuses a Code Assist account it suspended with a 403 of its own,
// on Antigravity and Gemini CLI alike, and it answers the account's own
// asks — loadCodeAssist for its project and its tiers, fetchAvailableModels
// for its models — with it, outside the gateway's request path. The
// gateway reads the refusal of a request (googleSuspended); discovery
// reads it here, and both tell one suspension alike.

import (
	"net/http"
	"regexp"
)

// googleToS is Google's 403 PERMISSION_DENIED for a Code Assist account it
// suspended: "This service has been disabled in this account for violation
// of Terms of Service. Please submit an appeal to continue using this
// product." (bestony on Discord, an Antigravity account, 11-06).
var googleToS = regexp.MustCompile(`(?i)disabled in this account for violation of Terms of Service`)

// GoogleSuspended says status and said are Google's refusal of a Code
// Assist account it suspended for its terms: the account, not the model or
// the request, until an appeal goes through. The gateway reads its own
// refusals so (googleSuspended); discovery reads the vendor's answer to
// loadCodeAssist and fetchAvailableModels by it, so both paths tell one
// suspension alike (#5478).
func GoogleSuspended(status int, said string) bool {
	return status == http.StatusForbidden && googleToS.MatchString(said)
}

// DiscoveryRefused, set by the gateway, rests an account its vendor
// refused itself — seen by discovery: the account's project, its tiers or
// its models, asked of Google outside the request path (#5478). The
// gateway rests it restricted, as it rests one of its own refusals, so the
// Routing page shows it and lifts it the same way.
var DiscoveryRefused func(agent, user string, status int, said string)

// DiscoveryHeld, set by the gateway, is what an account resting as one its
// vendor restricted is held with — the vendor's words, while the rest
// stands, "" while it doesn't: discovery asks it before asking the vendor
// for the account's list, its tiers or its quota again, so a suspended
// account is sent nothing until the rest ends or is lifted.
var DiscoveryHeld func(agent, user string) string

// discoveryHeld is DiscoveryHeld's answer, "" when the gateway isn't in:
// nothing rests, nothing is held.
func discoveryHeld(agent, user string) string {
	if DiscoveryHeld == nil {
		return ""
	}
	return DiscoveryHeld(agent, user)
}

// suspended tells the gateway, when err is Google's refusal of the account
// itself for its terms (GoogleSuspended), that discovery saw it: the
// gateway rests the account restricted, and discovery asks Google nothing
// more of it while it rests (#5478). It says whether err was that
// refusal.
func (g googleAccount) suspended(err error) bool {
	e, ok := err.(*codeAssistError)
	if !ok || !GoogleSuspended(e.status, e.msg) {
		return false
	}
	if DiscoveryRefused != nil {
		DiscoveryRefused(g.app.agent, g.user, e.status, e.msg)
	}
	return true
}
