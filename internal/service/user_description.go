package service

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// describeUser fills the facts Slack's user object reports that are not stored
// with the user: the member's workspace role and guest tier, and the bot and
// app a bot user belongs to. users.info reported every account as a person with
// no role, so an app could not tell its own bot user, an owner, or a guest from
// anyone else. It also reads the language the member chose, which
// include_locale reports.
func (m Messages) describeUser(ctx context.Context, user domain.User) (domain.User, error) {
	membership, err := m.Store.GetWorkspaceMembership(ctx, user.WorkspaceID, user.ID)
	switch {
	case err == nil:
		user.Role, user.Restricted, user.UltraRestricted, user.PrimaryOwner = membership.Role, membership.Restricted, membership.UltraRestricted, membership.PrimaryOwner
	case !errors.Is(err, store.ErrNotFound):
		return domain.User{}, err
	}
	bot, err := m.Store.GetBotByUser(ctx, user.WorkspaceID, user.ID)
	switch {
	case err == nil:
		user.BotID, user.AppID = bot.ID, bot.AppID
	case !errors.Is(err, store.ErrNotFound):
		return domain.User{}, err
	}
	preferences, err := m.Store.MemberPreferences(ctx, user.WorkspaceID, user.ID)
	switch {
	case err == nil:
		user.Locale = preferences[domain.LanguagePreference]
	case !errors.Is(err, store.ErrNotFound):
		return domain.User{}, err
	}
	return user, nil
}

// describedUser adapts describeUser to a (user, error) result.
func (m Messages) describedUser(ctx context.Context) func(domain.User, error) (domain.User, error) {
	return func(user domain.User, err error) (domain.User, error) {
		if err != nil {
			return domain.User{}, err
		}
		return m.describeUser(ctx, user)
	}
}

// describeUsers is describeUser for every member of a page.
func (m Messages) describeUsers(ctx context.Context, page domain.UserPage, err error) (domain.UserPage, error) {
	if err != nil {
		return domain.UserPage{}, err
	}
	for index, user := range page.Users {
		if page.Users[index], err = m.describeUser(ctx, user); err != nil {
			return domain.UserPage{}, err
		}
	}
	return page, nil
}
