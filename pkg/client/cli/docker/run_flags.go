package docker

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
)

type Volume struct {
	Name    string
	Target  string
	Options string
}

func (v *Volume) String() string {
	n := v.Name
	if n == "" {
		n = v.Target
	} else {
		n += ":" + v.Target
	}
	if v.Options != "" {
		n += ":" + v.Options
	}
	return n
}

type Mount struct {
	Type    string
	Source  string
	Target  string
	Options string
}

type Network struct {
	Name    string
	Aliases []string
}

func (m *Mount) String() string {
	sb := new(strings.Builder)
	sb.WriteString("type=")
	sb.WriteString(m.Type)
	if m.Source != "" {
		sb.WriteString(",src=")
		sb.WriteString(m.Source)
	}
	if m.Target != "" {
		sb.WriteString(",dst=")
		sb.WriteString(m.Target)
	}
	if m.Options != "" {
		sb.WriteByte(',')
		sb.WriteString(m.Options)
	}
	return sb.String()
}

type RunFlags struct {
	PublishedPorts PublishedPorts // --publish Port mappings that the container will expose on localhost
	Networks       []string
	Volumes        []Volume
	Mounts         []Mount
}

func ParseRunFlags(args []string) (*RunFlags, []string, error) {
	f := RunFlags{}
	values, err := flags.GetUnparsedValues("volume", 'v', args)
	if err != nil {
		return nil, nil, err
	}
	for _, av := range values {
		vx := strings.Split(av, ":")
		v := Volume{}
		switch len(vx) {
		case 1:
			v.Target = vx[0]
		case 2:
			v.Name = vx[0]
			v.Target = vx[1]
		case 3:
			v.Name = vx[0]
			v.Target = vx[1]
			v.Options = vx[2]
		default:
			return nil, nil, fmt.Errorf("invalid volume format: %s", av)
		}
		f.Volumes = append(f.Volumes, v)
	}
	values, err = flags.GetUnparsedValues("mount", 0, args)
	if err != nil {
		return nil, nil, err
	}
	for _, av := range values {
		m := Mount{}
		for _, vx := range strings.Split(av, ",") {
			kv := strings.Split(vx, "=")
			if len(kv) != 2 {
				return nil, nil, fmt.Errorf("invalid mount format: %s", av)
			}
			key := kv[0]
			val := kv[1]
			switch key {
			case "type":
				m.Type = val
			case "src", "source":
				m.Source = val
			case "destination", "dst", "target":
				m.Target = val
			default:
				if len(m.Options) > 0 {
					m.Options += ","
				}
				m.Options += vx
			}
		}
		f.Mounts = append(f.Mounts, m)
	}
	for {
		var av string
		var found bool
		av, found, args, err = flags.ConsumeUnparsedValue("publish", 'p', false, args)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			break
		}
		var pp PublishedPort
		pp, err = parsePublishedPort(av)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid port format for --publish: %w", err)
		}
		f.PublishedPorts = append(f.PublishedPorts, pp)
	}
	for {
		var av string
		var found bool
		av, found, args, err = flags.ConsumeUnparsedValue("expose", 0, false, args)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			break
		}
		// Convert --expose values to --publish values
		if strings.Contains(av, ":") {
			return nil, nil, fmt.Errorf("invalid port format for --expose: %s", av)
		}
		proto, portRange := nat.SplitProtoPort(av)
		start, end, err := nat.ParsePortRange(portRange)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid argument for --expose: %s, error: %s", av, err)
		}
		if start != end {
			return nil, nil, fmt.Errorf("invalid argument for --expose: %s, error: a range not supported", av)
		}
		port := uint16(start)
		f.PublishedPorts = append(f.PublishedPorts, PublishedPort{
			HostAddrPort:  netip.AddrPortFrom(netip.IPv4Unspecified(), port),
			Protocol:      proto,
			ContainerPort: port,
		})
	}
	for {
		var av string
		var found bool
		av, found, args, err = flags.ConsumeUnparsedValue("network", 0, false, args)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			break
		}
		f.Networks = append(f.Networks, av)
	}
	return &f, args, nil
}

func ConnectNetworksToDaemon(ctx context.Context, daemonName string, networks []Network) (context.CancelFunc, error) {
	cancel := func() {}
	if len(networks) == 0 {
		return cancel, nil
	}

	cli, err := docker.GetClient(ctx)
	if err != nil {
		return cancel, err
	}

	var ds []string
	cancel = func() {
		disconnectDaemons(ctx, cli, ds, daemonName)
	}
	for _, n := range networks {
		connected, err := connectDaemon(ctx, cli, daemonName, n)
		if err != nil {
			return cancel, err
		}
		if connected {
			ds = append(ds, n.Name)
		}
	}
	return cancel, nil
}

// connectDaemon connects the given network to the containerized daemon. It will
// return false if the daemon already had this network attached.
func connectDaemon(ctx context.Context, cli *client.Client, daemonName string, n Network) (bool, error) {
	var es *network.EndpointSettings
	if len(n.Aliases) > 0 {
		es = &network.EndpointSettings{
			Aliases: n.Aliases,
		}
	}
	dlog.Debugf(ctx, "Connecting network %s to container %s with settings %+v", n.Name, daemonName, es)
	if err := cli.NetworkConnect(ctx, n.Name, daemonName, es); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return false, nil
		}
		return false, fmt.Errorf("failed to connect network %s to container %s: %v", n.Name, daemonName, err)
	}
	return true, nil
}

// disconnectDaemons disconnects the given networks from the containerized daemon.
func disconnectDaemons(ctx context.Context, cli *client.Client, networks []string, daemonName string) {
	ctx = context.WithoutCancel(ctx)
	for _, n := range networks {
		dlog.Debugf(ctx, "Disconnecting network %s from container %s", n, daemonName)
		err := cli.NetworkDisconnect(ctx, n, daemonName, false)
		if err != nil {
			dlog.Warnf(ctx, "failed to disconnect network %s from daemon: %v", n, err)
		}
	}
}
