package grpc

import (
	"context"

	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

func (r Remote) BeginShortTokenRotation(ctx context.Context, clientID, clientSecret, token string) (string, error) {
	out, err := r.oauth.BeginShortTokenRotation(ctx, &chatv1.ShortTokenRotationRequest{ClientId: clientID, ClientSecret: clientSecret, Token: token})
	if err != nil {
		return "", err
	}
	return out.GetToken(), nil
}

func (r Remote) CompleteShortTokenRotation(ctx context.Context, clientID, clientSecret, token, newToken string) (string, error) {
	out, err := r.oauth.CompleteShortTokenRotation(ctx, &chatv1.ShortTokenRotationRequest{ClientId: clientID, ClientSecret: clientSecret, Token: token, NewToken: newToken})
	if err != nil {
		return "", err
	}
	return out.GetToken(), nil
}

func (s *Server) BeginShortTokenRotation(ctx context.Context, input *chatv1.ShortTokenRotationRequest) (*chatv1.ShortTokenRotationResponse, error) {
	token, err := s.implementation.BeginShortTokenRotation(ctx, input.GetClientId(), input.GetClientSecret(), input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ShortTokenRotationResponse{Token: token}, nil
}

func (s *Server) CompleteShortTokenRotation(ctx context.Context, input *chatv1.ShortTokenRotationRequest) (*chatv1.ShortTokenRotationResponse, error) {
	token, err := s.implementation.CompleteShortTokenRotation(ctx, input.GetClientId(), input.GetClientSecret(), input.GetToken(), input.GetNewToken())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ShortTokenRotationResponse{Token: token}, nil
}
