package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	compose "github.com/compose-spec/compose-go/v2/types"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	dockerCli "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Extension is the "x-telepresence" Compose Service extension
type Extension struct {
	Type      types.EngagementType `json:"type,omitempty"`
	Workload  string               `json:"workload,omitempty"`
	Name      string               `json:"name,omitempty"`
	Namespace string               `json:"namespace,omitempty"`
	Container string               `json:"container,omitempty"`
	Service   string               `json:"service,omitempty"`
	Ports     []types.PortMapping  `json:"ports,omitempty"`
	ToPod     []types.PortAndProto `json:"to_pod,omitempty"`
}

// ServiceNetworkProperties holds the network properties that a service must transfer to
// the daemon container.
type ServiceNetworkProperties struct {
	// hostname declared by the docker compose service.
	Hostname string

	// networks that the docker compose service declared, along with
	// aliases. Aliases affect DNS.
	Networks []dockerCli.Network

	// ports declared by the docker compose service
	Ports []compose.ServicePortConfig
}

type Engagement struct {
	Extension
	ComposeService    string
	NetworkProperties ServiceNetworkProperties

	// Volumes is a map of volume names keyed by target paths. This is so that they
	// can properly replaced by volumes provided by the engagement.
	Volumes map[string]*compose.ServiceVolumeConfig
}

type Engagements map[string]*Engagement

type ActiveEngagement struct {
	*Engagement
	Environment map[string]string
	DaemonID    *daemon.Identifier
	SftpPort    uint16
	Mounts      types.MountPolicies
	Volumes     map[string]string
	done        chan error
	IPAMConfig  []*compose.IPAMPool
	DaemonIP    string
}

func (e *Engagement) Connect(ctx context.Context) (context.Context, error) {
	cr := daemon.GetRequest(ctx)
	cr = cr.Clone()
	if e.Namespace != "" {
		cr.KubeFlags["namespace"] = e.Namespace
	}
	cr.NetworkAliases = []string{e.ComposeService}
	cr.Docker = true
	cr.Name = e.Name
	np := &e.NetworkProperties
	cr.Hostname = np.Hostname

	ctx = daemon.WithRequest(ctx, cr)
	dlog.Debugf(ctx, "Connecting to %s", e.Namespace)

	// TODO: teleroutePort
	ctx, err := connect.EnsureUserDaemon(ctx, true, 0)
	if err != nil {
		return ctx, err
	}

	// TODO: teleroutePort
	ctx, err = connect.EnsureSession(ctx, "compose", true, 0)
	if err != nil {
		return ctx, err
	}
	return ctx, nil
}

func (e *Engagement) CreateIngestRequest(localMountPort uint16) *connector.IngestRequest {
	ir := &connector.IngestRequest{
		Identifier: &connector.IngestIdentifier{
			WorkloadName:  e.Workload,
			ContainerName: e.Container,
		},
		LocalMountPort: int32(localMountPort),
	}
	for _, toPod := range e.ToPod {
		ir.LocalPorts = append(ir.LocalPorts, toPod.String())
	}
	return ir
}

func (e *Engagement) CreateInterceptRequest(localMountPort uint16) (*connector.CreateInterceptRequest, error) {
	spec := &manager.InterceptSpec{
		Name:          e.Name,
		ServiceName:   e.Service,
		ContainerName: e.Container,
		Mechanism:     "tcp",
		Agent:         e.Workload,
		TargetHost:    "127.0.0.1",
		Replace:       e.Type == types.EngagementTypeReplace,
		NoDefaultPort: e.Type == types.EngagementTypeReplace,
		Wiretap:       e.Type == types.EngagementTypeWiretap,
	}
	ir := &connector.CreateInterceptRequest{
		Spec:           spec,
		LocalMountPort: int32(localMountPort),
		MountReadOnly:  e.Type == types.EngagementTypeWiretap,
	}
	for _, toPod := range e.ToPod {
		spec.LocalPorts = append(spec.LocalPorts, toPod.String())
	}

	if len(e.Ports) == 0 {
		switch e.Type {
		case types.EngagementTypeIntercept, types.EngagementTypeWiretap:
			return nil, fmt.Errorf("A %s requires at least one port", e.Type)
		case types.EngagementTypeReplace:
			spec.PortIdentifier = "all"
		default:
		}
		return ir, nil
	}

	p0 := e.Ports[0]
	spec.PortIdentifier = p0.From().String()
	spec.TargetPort = int32(p0.To().Port)
	for i := 1; i < len(e.Ports); i++ {
		pm := e.Ports[i].String()
		if colIdx := strings.IndexByte(pm, ':'); colIdx > 0 {
			// Entries in the "ports" list puts local port first, but it's the destination in the pod-port mapping.
			to := pm[:colIdx]
			from := pm[colIdx+1:]
			if slashIdx := strings.IndexByte(from, '/'); slashIdx > 0 {
				from = from[:slashIdx]
				to += from[slashIdx:]
			}
			pm = from + ":" + to
		}
		if err := types.PortMapping(pm).Validate(); err != nil {
			return nil, err
		}
		spec.PodPorts = append(spec.PodPorts, pm)
	}
	return ir, nil
}

func (e *Engagement) Activate(ctx context.Context) (*ActiveEngagement, error) {
	ud := daemon.GetUserClient(ctx)
	if !ud.Containerized() {
		return nil, errors.New("user daemon is not running in a container")
	}

	daemonID := ud.DaemonID()
	// TODO: Setup teleroute network
	_, daemonIP, err := docker.ContainerPidAndIP(ctx, daemonID.ContainerName())
	if err != nil {
		return nil, err
	}

	ae := &ActiveEngagement{
		Engagement: e,
		DaemonID:   daemonID,
		DaemonIP:   daemonIP,
		done:       make(chan error),
	}

	if e.Extension.Type == types.EngagementTypeConnect {
		return ae, nil
	}

	lma, err := client.FreePortsTCP(1)
	if err != nil {
		return nil, err
	}
	ae.SftpPort = uint16(lma[0].Port)

	if e.Extension.Type == types.EngagementTypeIngest {
		ir := e.CreateIngestRequest(ae.SftpPort)

		// Submit the request
		ii, err := ud.Ingest(ctx, ir)
		if err != nil {
			switch grpcStatus.Code(err) {
			case grpcCodes.AlreadyExists, grpcCodes.NotFound, grpcCodes.Unimplemented, grpcCodes.FailedPrecondition:
				return nil, errors.New(grpcStatus.Convert(err).Message())
			}
			return nil, fmt.Errorf("ingest: %w", err)
		}
		env := make(map[string]string)
		maps.Merge(env, ii.Environment)
		ae.Environment = env
		ae.Mounts = types.MountPoliciesFromRPC(ii.Mounts)
		return ae, nil
	}

	ir, err := e.CreateInterceptRequest(ae.SftpPort)
	if err != nil {
		return nil, err
	}
	r, err := ud.CreateIntercept(ctx, ir)
	if err = intercept.Result(r, err); err != nil {
		return nil, fmt.Errorf("connector.CreateIntercept: %w", err)
	}

	ii := r.InterceptInfo
	env := make(map[string]string)
	maps.Merge(env, ii.Environment)
	ae.Environment = env
	ae.Mounts = types.MountPoliciesFromRPC(ii.Mounts)
	return ae, nil
}

func (e *Engagement) desiredRemoteMounts(remoteMounts types.MountPolicies) (types.MountPolicies, error) {
	desiredMounts := make(types.MountPolicies, len(remoteMounts))
	for p, m := range remoteMounts {
		if _, ok := e.Volumes[p]; ok {
			desiredMounts[p] = m
		}
	}
	return desiredMounts, nil
}

func (a *ActiveEngagement) createVolumes(ctx context.Context) error {
	ro := true
	switch a.Extension.Type {
	case types.EngagementTypeIntercept, types.EngagementTypeReplace:
		ro = false
	default:
	}
	vols, err := docker.CreateVolumes(ctx, a.DaemonID.ContainerName(), a.SftpPort, a.Environment["TELEPRESENCE_CONTAINER"], a.Mounts, ro)
	if err != nil {
		return err
	}
	a.Volumes = vols
	return nil
}

func (a *ActiveEngagement) Wait(ctx context.Context) error {
	defer func() {
		progress.Write(ctx, progress.WorkingEvent(a.ComposeService, "Disconnecting"))
		connect.Disconnect(context.WithoutCancel(ctx))
		progress.Write(ctx, progress.DoneEvent(a.ComposeService, "Disconnected"))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-a.done:
		return err
	}
}
