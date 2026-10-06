package service

import (
	"context"
	"strconv"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Workspace permissions. Each setting is applied here, where the behavior it
// governs is decided, so the administration page never draws a switch that
// changes nothing.

// WorkspacePolicy reads the workspace's permissions. Any member may read it:
// the composer needs to know whether to ask before a broadcast mention, and
// the create-channel dialog needs to know whether to offer a private channel.
// Neither tells a member anything they could not learn by trying.
func (m Messages) WorkspacePolicy(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (domain.WorkspacePolicy, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.WorkspacePolicy{}, err
	}
	return m.Store.GetWorkspacePolicy(ctx, workspaceID)
}

// SetWorkspacePolicy replaces the workspace's permissions. It is an
// administrator's decision, as Slack's help centre describes for each setting.
func (m Messages) SetWorkspacePolicy(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, policy domain.WorkspacePolicy) (domain.WorkspacePolicy, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.WorkspacePolicy{}, err
	}
	if !policy.Valid() {
		return domain.WorkspacePolicy{}, domain.ErrInvalidWorkspacePolicy
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("workspace.policy_changed",
		events.String("broadcast_warning_off", strconv.FormatBool(policy.BroadcastWarningOff)),
		events.String("private_channel_creators", string(policy.PrivateChannelCreators)),
	), time.Now().UTC())
	if err != nil {
		return domain.WorkspacePolicy{}, err
	}
	if err := m.Store.SetWorkspacePolicy(ctx, workspaceID, policy, event); err != nil {
		return domain.WorkspacePolicy{}, err
	}
	return policy, nil
}

// requirePrivateChannelCreator refuses a member the "who can create private
// channels" policy leaves out. Creating a private channel and converting a
// group DM into one both make a private channel, so both ask this. It reads
// the actor's membership itself, so every caller is guarded by one call.
func (m Messages) requirePrivateChannelCreator(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID) error {
	membership, err := m.activeWorkspaceMembership(ctx, workspaceID, actor)
	if err != nil {
		return err
	}
	policy, err := m.Store.GetWorkspacePolicy(ctx, workspaceID)
	if err != nil {
		return err
	}
	if !policy.PrivateChannelCreators.Admits(membership.Role) {
		return domain.ErrPrivateChannelCreationRestricted
	}
	return nil
}
