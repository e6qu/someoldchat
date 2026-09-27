package slack

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/realtime"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
)

// Surface is the whole Slack-compatible surface one process serves: the Web
// API under /api/, the Socket Mode WebSocket at /socket-mode that
// apps.connections.open hands out, and the RTM WebSocket at /rtm that
// rtm.connect hands out.
//
// It exists so that cmd/server and the official-SDK qualification fixture
// compose that surface with the same code. They used to wire it separately,
// and the production wiring configured Socket Mode after Register had already
// bound every route to a copy of the handler: apps.connections.open answered
// socket_mode_unavailable in every real deployment while qualification, whose
// fixture wired it in the other order, passed.
type Surface struct {
	Messages      chatapi.Service
	Authenticator auth.Authenticator
	// AppAuthenticator authenticates app-level (xapp-) tokens. When set, the
	// surface serves Socket Mode: apps.connections.open and /socket-mode.
	AppAuthenticator auth.Authenticator
	// SocketHost and SocketTLS override the origin Socket Mode connection URLs
	// name. Empty SocketHost follows the origin the client reached
	// apps.connections.open on; see socketmode.Service.
	SocketHost string
	SocketTLS  bool
	// Responses receives Socket Mode response payloads. Nil selects Messages;
	// the qualification fixture decorates it to observe what apps answered.
	Responses socketmode.ResponseSink
	// Limiter enforces the Web API rate-limiting contract when set.
	Limiter *RateLimiter
	// PublicURL is the deployment's configured public origin, the base of
	// every absolute URL the Web API emits. Empty follows the request origin.
	PublicURL string
	Logger    *slog.Logger
}

// Mount registers the surface on mux.
func Mount(mux *http.ServeMux, surface Surface) error {
	if mux == nil {
		return errors.New("Slack surface requires a mux")
	}
	if surface.Messages == nil {
		return errors.New("Slack surface requires a chat service")
	}
	var options []HandlerOption
	if surface.AppAuthenticator != nil {
		options = append(options,
			WithAppAuthenticator(surface.AppAuthenticator),
			WithSocketMode(socketmode.Service{Store: surface.Messages, Host: surface.SocketHost, TLS: surface.SocketTLS}))
	}
	handler, err := NewHandler(surface.Messages, surface.Authenticator, options...)
	if err != nil {
		return err
	}
	handler.Limiter = surface.Limiter
	if err := handler.SetPublicURL(surface.PublicURL); err != nil {
		return err
	}
	handler.Register(mux)
	if surface.AppAuthenticator != nil {
		responses := surface.Responses
		if responses == nil {
			responses = surface.Messages
		}
		mux.Handle("/socket-mode", socketmode.Handler{
			Store: surface.Messages, Queue: surface.Messages, Interactions: surface.Messages,
			Responses: responses, Logger: surface.Logger,
		})
	}
	rtm, err := realtime.NewRTMHandler(surface.Messages, surface.Messages, surface.Messages, surface.Messages)
	if err != nil {
		return err
	}
	rtm.Logger = surface.Logger
	rtm.RegisterRTM(mux)
	return nil
}
