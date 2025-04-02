package compose

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/go-json-experiment/json"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

const (
	ConnectExtension   = "x-connect"
	IngestExtension    = "x-ingest"
	WiretapExtension   = "x-wiretap"
	InterceptExtension = "x-intercept"
	ReplaceExtension   = "x-replace"
)

var extensions = []string{
	ConnectExtension, IngestExtension, WiretapExtension, InterceptExtension, ReplaceExtension,
}

type Transformer interface {
	ApplyEngagements(engagements map[string]*ActiveEngagement) error

	// Engagements will retrieve and return all engagements found under the "x-telepresence" tag.
	Engagements() (Engagements, error)

	// MarshalYAML returns a YAML encoded image of the project.
	MarshalYAML() ([]byte, error)

	// RunProject runs the possibly transformed project by doing a "docker compose up"
	RunProject(ctx context.Context, engagements map[string]*ActiveEngagement, startTime time.Time) error

	// WithProfiles disables services which don't match selected profiles.
	WithProfiles(profiles []string) error
}

type transformer struct {
	config  *Config
	project *compose.Project
}

func (t *transformer) WithProfiles(profiles []string) error {
	p, err := t.project.WithProfiles(profiles)
	if err == nil {
		t.project = p
	}
	return err
}

func networks(ns map[string]*compose.ServiceNetworkConfig) []docker.Network {
	if len(ns) == 0 {
		return nil
	}
	networks := make([]docker.Network, len(ns))
	i := 0
	for n, s := range ns {
		nw := docker.Network{Name: n}
		if s != nil {
			nw.Aliases = s.Aliases
		}
		networks[i] = nw
		i++
	}
	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Name < networks[j].Name
	})
	return networks
}

func createEngagement(sv *compose.ServiceConfig, et types.EngagementType, ex map[string]any) (*Engagement, error) {
	// ex is a value of any type, but we only accept a map[string]any here.
	data, err := json.Marshal(ex)
	if err != nil {
		return nil, err
	}
	vols := make(map[string]*compose.ServiceVolumeConfig, len(sv.Volumes))
	for _, v := range sv.Volumes {
		vols[v.Target] = &v
	}
	eg := Engagement{
		ComposeService: sv.Name,
		NetworkProperties: ServiceNetworkProperties{
			Hostname: sv.Hostname,
			Networks: networks(sv.Networks),
			Ports:    sv.Ports,
		},
		Volumes: vols,
	}
	err = json.Unmarshal(data, &eg.Extension, json.RejectUnknownMembers(true))
	if err != nil {
		return nil, err
	}
	eg.Type = et

	// Name defaults to the name of the docker-compose service
	if eg.Name == "" {
		if eg.Workload == "" {
			eg.Name = sv.Name
		} else {
			eg.Name = eg.Workload
		}
	}
	// Workload defaults to the name
	if eg.Workload == "" {
		eg.Workload = eg.Name
	}
	return &eg, nil
}

func (t *transformer) Engagements() (engagements Engagements, err error) {
	if len(t.config.Services) > 0 {
		t.project, err = t.project.WithSelectedServices(t.config.Services)
		if err != nil {
			return nil, err
		}
	}
	for n, sv := range t.project.Services {
		var ex map[string]any
		var et types.EngagementType
		for _, en := range extensions {
			if x, ok := sv.Extensions[en].(map[string]any); ok {
				if ex != nil {
					return nil, fmt.Errorf("service %s can only be extended by one of %v", n, extensions)
				}
				et, err = types.ParseEngagementType(en[2:])
				if err != nil {
					return nil, fmt.Errorf("internal error: %v", err)
				}
				ex = x
			}
		}
		if ex != nil {
			eg, err := createEngagement(&sv, et, ex)
			if err != nil {
				return nil, err
			}
			if engagements == nil {
				engagements = make(Engagements)
			}
			engagements[n] = eg
		}
	}
	return engagements, nil
}

// ApplyEngagements performs the following actions:
//
//  1. Create a new service using the original engagement name, which runs a telepresence daemon.
//  3. Move hostname, networks, and ports from the original service to the daemon service.
//  4. Set the NetworkMode of the original service to "container:<name>", so that it shares the
//     network of the new daemon service.
func (t *transformer) ApplyEngagements(engagements map[string]*ActiveEngagement) error {
	if len(engagements) == 0 {
		return nil
	}

	// WithServiceDisabled performs a deepCopy of the project when called without arguments. We
	// want the original Project intact.
	p := t.project.WithServicesDisabled()
	sm := p.Services
	for n, e := range engagements {
		if s, ok := sm[n]; ok {
			s.NetworkMode = "container:" + e.DaemonID.ContainerName()
			s.Networks = nil
			s.Ports = nil
			s.Hostname = ""
			sm[n] = s
		} else {
			return fmt.Errorf("unknown service %q", n)
		}
	}

	// Let all services attach to the "telepresence" network. This is the network where the active
	// engagements publish the name of the docker-compose service that they engage on behalf of.
	for n, s := range sm {
		if _, ok := engagements[n]; !ok {
			// Unengaged service
			if _, ok := s.Networks["telepresence"]; !ok {
				s.Networks["telepresence"] = nil // Nil is a valid entry signifying a named network without aliases.
			}
		}
	}

	if p.Networks == nil {
		p.Networks = make(compose.Networks)
	}
	p.Networks["telepresence"] = compose.NetworkConfig{
		Name:     "telepresence",
		External: true,
	}

	err := p.CheckContainerNameUnicity()
	if err == nil {
		t.project = p
	}
	return err
}

func (t *transformer) MarshalYAML() ([]byte, error) {
	return t.project.MarshalYAML()
}

func (t *transformer) RunProject(ctx context.Context, engagements map[string]*ActiveEngagement, startTime time.Time) error {
	if len(engagements) > 0 {
		err := t.ApplyEngagements(engagements)
		if err != nil {
			return err
		}
	}

	yml, err := t.MarshalYAML()
	if err != nil {
		return err
	}
	mcf, err := os.CreateTemp("", "tpc-*.yaml")
	if err != nil {
		return err
	}
	mcp := mcf.Name()
	dlog.Debug(ctx, string(yml))
	_, err = mcf.Write(yml)
	mcf.Close()
	if err != nil {
		return err
	}
	c := t.config
	opts := make([]string, 0, 10)
	opts = append(opts, "compose", "--file", mcp)

	// Add "compose" specific options
	if c.ProjectName != "" {
		opts = append(opts, "--project-name", c.ProjectName)
	}
	if c.ProjectDir == "" {
		c.ProjectDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	opts = append(opts, "--project-directory", c.ProjectDir)
	if c.Progress != "auto" {
		opts = append(opts, "--progress", c.Progress)
	}
	for _, envFile := range c.EnvFiles {
		opts = append(opts, "--env-file", envFile)
	}
	opts = append(opts, "up")
	opts = c.AppendComposeUpFlags(ctx, opts)

	// The "compose down" runs for multiple reasons, and we don't want this function to return until it's finished.
	downDone := make(chan struct{})
	defer func() {
		<-downDone
	}()

	quitCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	// Ensure termination if one of the daemon processes quit externally.
	for _, e := range engagements {
		go func() {
			if err := daemon.CancelWhenRmFromCache(ctx, cancel, e.DaemonID.InfoFileName()); err != nil {
				dlog.Error(ctx)
			}
		}()
	}

	go func() {
		<-quitCtx.Done()
		opts := []string{"compose", "--file", mcp, "--project-directory", c.ProjectDir}
		if c.Progress != "auto" {
			opts = append(opts, "--progress", c.Progress)
		}
		opts = append(opts, "down")
		_ = proc.StdCommand(context.WithoutCancel(ctx), "docker", opts...).Run()
		_ = os.Remove(mcp)
		close(downDone)
	}()

	// Use ctx here rather than quitCtx. The quitCtx is triggered by cancel, and will run "compose down" AFTER this command has
	// returned, unless the CancelWhenRmFromCache triggers a cancel in which case the "compose down" runs anyway, and consequently
	// terminates this "compose up"
	cmd := proc.StdCommand(ctx, "docker", opts...)
	err = cmd.Start()
	if err != nil {
		return err
	}
	return cmd.Wait()
}
