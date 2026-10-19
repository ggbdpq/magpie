package provider

import (
	"testing"
)

// Only a 401 whose words say the token sent has expired and is a refresh's
// to mend is refreshable (#2973): a revoked sign-in is not (it is #1793's
// to mark), and a 403, a 5xx or a rate limit says nothing about the token.
func TestSignInRefreshableWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, `{"error":{"message":"Your authentication token has expired. Please try refreshing it."}}`, true},
		{401, "Your authentication token has expired. Please try refreshing it.", true},
		{401, `{"error":{"code":"token_expired","message":"Provided authentication token is expired."}}`, true},
		{401, `{"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"}}`, false},
		{401, `{"error":{"code":"refresh_token_reused"}}`, false},
		{401, `{"error":{"message":"Incorrect API key provided"}}`, false},
		{403, `{"error":{"code":"token_expired"}}`, false},
		{500, `{"error":{"code":"token_expired"}}`, false},
		{429, `{"error":{"code":"token_expired"}}`, false},
	} {
		if got := SignInRefreshable(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
