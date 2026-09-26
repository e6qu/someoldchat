package store

import (
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// OAuthRedirectMatches is the redirect_uri rule for redeeming a code, shared by
// every repository. Slack's contract is "must match the originally submitted
// URI (if one was sent)": a redirect_uri named at authorization must be named
// again, identically, while an authorization that relied on the app's single
// configured redirect leaves the exchange free to omit it. The grant records
// the URI as the installer's request named it, or empty.
func OAuthRedirectMatches(authorized, presented string) bool {
	return authorized == "" || authorized == presented
}

// OAuthGrantIssue decides which credential a code redemption issues. The v2
// exchange asks for a bot token, but a grant that carries no bot scopes — a
// user-scope-only install — has no bot to issue for: Slack answers it with the
// installer's user token under authed_user and nothing at the top level. The
// installer's token, minted alongside the bot token for exactly this seat,
// then becomes the issued credential, and its refresh token and expiry come
// with it.
func OAuthGrantIssue(grant domain.OAuthCode, requested domain.TokenType, accessToken string, token domain.OAuthToken) (domain.TokenType, string, domain.OAuthToken, error) {
	if requested != domain.TokenBot || len(domain.NormalizeScopes(grant.BotScopes)) != 0 || grant.BotID != "" {
		return requested, accessToken, token, nil
	}
	if strings.TrimSpace(token.AuthedUserAccessToken) == "" {
		return "", "", domain.OAuthToken{}, InvalidArgument("missing installer user access token")
	}
	accessToken = token.AuthedUserAccessToken
	token.TokenType = domain.TokenUser
	token.RefreshToken = token.AuthedUserRefreshToken
	token.ExpiresAt = token.AuthedUserExpiresAt
	token.AuthedUserAccessToken = ""
	token.AuthedUserRefreshToken = ""
	token.AuthedUserExpiresAt = time.Time{}
	return domain.TokenUser, accessToken, token, nil
}
