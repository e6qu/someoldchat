package huddlesfu

import (
	"fmt"
	"io"
	"net"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// Config is how a deployment makes the in-process SFU reachable from outside the
// host. Both fields are optional: with neither set the SFU answers with host
// candidates, which reach it on the same network or the same machine — enough
// for local use and the tests, but not across the internet.
type Config struct {
	// PublicIP is the address browsers can reach this server on. When set it is
	// advertised as a 1-to-1 NAT mapping of the host candidate, so a server
	// behind a load balancer or NAT hands out an address that actually routes to
	// it. Empty leaves the host candidates unmapped.
	PublicIP string
	// UDPPort binds all huddle media to one UDP port through a shared mux, so a
	// deployment exposes a single, known port rather than an ephemeral range.
	// Zero uses ephemeral ports.
	UDPPort int
}

// buildAPI assembles the WebRTC API the rooms share: the default codecs, the
// standard interceptors, and a setting engine carrying the deployment's public
// address and shared UDP port. The returned closer releases the UDP socket, if
// one was opened, when the manager shuts down.
func buildAPI(config Config) (*webrtc.API, io.Closer, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, nil, fmt.Errorf("register codecs: %w", err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, registry); err != nil {
		return nil, nil, fmt.Errorf("register interceptors: %w", err)
	}

	settingEngine := webrtc.SettingEngine{}
	var closer io.Closer = io.NopCloser(nil)
	if config.PublicIP != "" {
		settingEngine.SetNAT1To1IPs([]string{config.PublicIP}, webrtc.ICECandidateTypeHost)
	}
	if config.UDPPort > 0 {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: config.UDPPort})
		if err != nil {
			return nil, nil, fmt.Errorf("bind huddle media UDP port %d: %w", config.UDPPort, err)
		}
		settingEngine.SetICEUDPMux(webrtc.NewICEUDPMux(nil, conn))
		closer = conn
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(settingEngine),
	)
	return api, closer, nil
}
