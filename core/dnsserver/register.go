package dnsserver

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/coreos/go-systemd/activation"
	"github.com/miekg/coredns/middleware"

	"github.com/mholt/caddy"
	"github.com/mholt/caddy/caddyfile"
)

const serverType = "dns"

func init() {
	flag.StringVar(&Port, "port", DefaultPort, "Default port")
	flag.BoolVar(&Quiet, "quiet", false, "Quiet mode (no initialization output)")

	caddy.RegisterServerType(serverType, caddy.ServerType{
		Directives: func() []string { return directives },
		DefaultInput: func() caddy.Input {
			return caddy.CaddyfileInput{
				Filepath:       "Corefile",
				Contents:       []byte(".:" + Port + " {\nwhoami\n}\n"),
				ServerTypeName: serverType,
			}
		},
		NewContext: newContext,
	})
}

func newContext() caddy.Context {
	return &dnsContext{keysToConfigs: make(map[string]*Config)}
}

type dnsContext struct {
	keysToConfigs map[string]*Config

	// configs is the master list of all site configs.
	configs []*Config
}

func (h *dnsContext) saveConfig(key string, cfg *Config) {
	h.configs = append(h.configs, cfg)
	h.keysToConfigs[key] = cfg
}

// InspectServerBlocks make sure that everything checks out before
// executing directives and otherwise prepares the directives to
// be parsed and executed.
func (h *dnsContext) InspectServerBlocks(sourceFile string, serverBlocks []caddyfile.ServerBlock) ([]caddyfile.ServerBlock, error) {
	// Normalize and check all the zone names and check for duplicates
	dups := map[string]string{}
	for _, s := range serverBlocks {
		for i, k := range s.Keys {
			za, err := normalizeZone(k)
			if err != nil {
				return nil, err
			}
			s.Keys[i] = za.String()
			if v, ok := dups[za.Zone]; ok {
				return nil, fmt.Errorf("cannot serve %s - zone already defined for %v", za, v)
			}
			dups[za.Zone] = za.String()

			// Save the config to our master list, and key it for lookups
			cfg := &Config{
				Zone: za.Zone,
				Port: za.Port,
			}
			h.saveConfig(za.String(), cfg)
		}
	}
	return serverBlocks, nil
}

// MakeServers uses the newly-created siteConfigs to create and return a list of server instances.
func (h *dnsContext) MakeServers() ([]caddy.Server, error) {

	// we must map (group) each config to a bind address
	groups, err := groupConfigsByListenAddr(h.configs)
	if err != nil {
		return nil, err
	}
	// then we create a server for each group
	var servers []caddy.Server
	for addr, group := range groups {
		s, err := NewServer(addr, group)
		if err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}

	return servers, nil
}

// AddMiddleware adds a middleware to a site's middleware stack.
func (c *Config) AddMiddleware(m middleware.Middleware) {
	c.Middleware = append(c.Middleware, m)
}

// groupSiteConfigsByListenAddr groups site configs by their listen
// (bind) address, so sites that use the same listener can be served
// on the same server instance. The return value maps the listen
// address (what you pass into net.Listen) to the list of site configs.
// This function does NOT vet the configs to ensure they are compatible.
func groupConfigsByListenAddr(configs []*Config) (map[string][]*Config, error) {
	groups := make(map[string][]*Config)

	for _, conf := range configs {
		if conf.Port == "" {
			conf.Port = Port
		}
		if conf.Port == "sa" {
			port, err := setupSockets()
			if err != nil {
				return nil, fmt.Errorf("Can't setup socket activation: %s", err.Error())
			}
			conf.Port = port
			conf.isSocketActivated = true
		}
		addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(conf.ListenHost, conf.Port))
		if err != nil {
			return nil, err
		}
		addrstr := addr.String()
		groups[addrstr] = append(groups[addrstr], conf)
	}

	return groups, nil
}

func setupSockets() (string, error) {
	if socketActivatedListener == nil {
		listeners, err := activation.Listeners(false)
		if err != nil {
			return "", err
		}
		packetConns, err := activation.PacketConns(true)
		if err != nil {
			return "", err
		}
		for _, l := range listeners {
			if l != nil {
				socketActivatedListener = l
			}
		}
		for _, p := range packetConns {
			if p != nil {
				socketActivatedPacketConn = p
			}
		}
		if socketActivatedListener == nil {
			return "", errors.New("No listeners")
		}
		if socketActivatedPacketConn == nil {
			return "", errors.New("No packet connections")
		}
	}
	port := socketActivatedListener.Addr().(*net.TCPAddr).Port
	return strconv.Itoa(port), nil
}

const (
	// DefaultPort is the default port.
	DefaultPort = "2053"
)

// These "soft defaults" are configurable by
// command line flags, etc.
var (
	// Port is the site port
	Port = DefaultPort

	// GracefulTimeout is the maximum duration of a graceful shutdown.
	GracefulTimeout time.Duration

	// Quiet mode will not show any informative output on initialization.
	Quiet bool

	socketActivatedListener   net.Listener
	socketActivatedPacketConn net.PacketConn
)
