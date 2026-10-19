package provider

// A sign-in its vendor answered 401 "the token has expired, please try
// refreshing it" (#2973) is not a revoked one: a refresh mends it, as
// Codex's own does on a 401 — and the token's JWT exp said nothing, the
// vendor had retired it all the same. Where #1793 marks a revoked
// credential and passes the account over, this renews the sign-in in
// place, once: bounded, and merged with any refresh already going (the
// same lock a request's signing waits on), so the next request goes out
// with the fresh token and the account isn't benched while its allowance
// is still there. A refresh the vendor refuses a saved account is kept on
// it as lapsed (savedRefreshFailed); the Codex home's own sign-in is left
// as its keeper has it, its next request reading it anew.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// refreshableWords: a 401 that says the token sent has expired and is a
// refresh's to mend: OpenAI's own code for one, and the words the ChatGPT
// backend answered a token retired on its side with (#2973).
var refreshableWords = regexp.MustCompile(`(?i)\btoken_expired\b|authentication token has expired`)

// SignInRefreshable says a reply is the vendor's word that the token the
// request went with has expired and its sign-in is to be refreshed.
func SignInRefreshable(status int, body []byte) bool {
	return status == http.StatusUnauthorized && refreshableWords.Match(body)
}

// NoteRefreshableSignIn renews the sign-in the request req went to
// account a with, the vendor having said its token had expired (#2973).
// A sign-in renewed since the request went out is left as it is, so
// concurrent 401s on one account spend one rotation, not one apiece.
func NoteRefreshableSignIn(ctx context.Context, a *Account, req *http.Request) {
	if !RevokedAccount(a) || a.Agent != "codex" {
		return
	}
	sent := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if a.token != nil {
		// a saved account: renewed where magpie keeps it
		if tok, _, err := savedLoginToken(rctx, a.Agent, a.User); err == nil && tok != "" && tok != sent {
			return
		}
		_, _, _ = renewSavedLogin(rctx, a.Agent, a.User, true)
		return
	}
	// the Codex home's own sign-in: renewed where Codex keeps it
	if tok, _, err := codexToken(rctx, codexHome()); err == nil && tok != "" && tok != sent {
		return
	}
	codexTokenRefresh(rctx, codexHome(), true)
}
