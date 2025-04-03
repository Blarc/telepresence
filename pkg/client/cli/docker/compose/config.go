package compose

import (
	"context"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Config struct {
	// Compose options
	ProjectDir  string
	ProjectName string
	Progress    string
	ConfigPaths []string
	EnvFiles    []string
	Profiles    []string
	Services    []string

	// Compose up options
	ComposeFlags   *pflag.FlagSet
	ComposeUpFlags *pflag.FlagSet
}

// toProjectOptions was shamelessly copied from https://github.com/docker/compose/blob/main/cmd/compose/compose.go.
// Kudos to the Docker Compose CLI authors.
func (c *Config) toProjectOptions(po ...cli.ProjectOptionsFn) (*cli.ProjectOptions, error) {
	return cli.NewProjectOptions(c.ConfigPaths,
		append(po,
			cli.WithWorkingDirectory(c.ProjectDir),
			// First apply os.Environment, always win
			cli.WithOsEnv,
			// Load PWD/.env if present and no explicit --env-file has been set
			cli.WithEnvFiles(c.EnvFiles...),
			// read the dot-env file to populate project environment
			cli.WithDotEnv,
			// get the compose-file path set by COMPOSE_FILE
			cli.WithConfigFileEnv,
			// if none was selected, get the default compose.yaml file from current dir or parent folder
			cli.WithDefaultConfigPath,
			// ... and then, a project directory != PWD maybe has been set, so let's load .env file
			cli.WithEnvFiles(c.EnvFiles...),
			cli.WithDotEnv,
			// eventually COMPOSE_PROFILES should have been set
			cli.WithDefaultProfiles(c.Profiles...),
			cli.WithName(c.ProjectName))...)
}

// Connection specific data must be added to the x-telepresence annotation in the compose file and will be ignored here.
// CIDR conflicts are not applicable when using a containerized daemon.
var hiddenFlags = []string{
	"allow-conflicting-subnets", "docker", "expose", "hostname", "name", "namespace", "proxy-via", "vnat",
}

func (c *Config) AddFlags(cmd *cobra.Command) {
	// The --docker flag is implicit.
	for _, hide := range hiddenFlags {
		cmd.Flag(hide).Hidden = true
	}
	_ = cmd.Flag("docker").Value.Set("true")

	cfs := cmd.Flags()
	cFlags := pflag.NewFlagSet("Compose flags", pflag.ContinueOnError)
	cFlags.StringArrayVarP(&c.ConfigPaths, "file", "f", []string{}, "Compose configuration files")
	cFlags.StringArrayVar(&c.EnvFiles, "env-file", []string{}, "Optional environment files")
	cFlags.StringVar(&c.ProjectDir, "project-directory", "", "Specify an alternate working directory (default: the path of the, first specified, Compose file)")
	cFlags.StringVar(&c.ProjectName, "project-name", "", "Project name")
	cFlags.StringArrayVar(&c.Profiles, "profile", []string{}, "Profile to enable")
	cfs.AddFlagSet(cFlags)
	c.ComposeFlags = cFlags

	upFlags := pflag.NewFlagSet("Compose Up flags", pflag.ContinueOnError)
	upFlags.Bool("abort-on-container-exit", false, "Stops all containers if any container was stopped")
	upFlags.Bool("abort-on-container-failure", false, "Stops all containers if any container exited with failure")
	upFlags.Bool("build", false, "Build images before starting containers")
	upFlags.String("exit-code-from", "", "Return the exit code of the selected service container. Implies --abort-on-container-exit")
	upFlags.Bool("force-recreate", false, "Recreate containers even if their configuration and image haven't changed")
	upFlags.Bool("no-build", false, "Don't build an image, even if it's policy")
	upFlags.Bool("no-color", false, "Produce monochrome output")
	upFlags.Bool("no-deps", false, "Don't start linked services")
	upFlags.Bool("no-log-prefix", false, "Don't print prefix in logs")
	upFlags.Bool("no-recreate", false, "If containers already exist, don't recreate them. Incompatible with --force-recreate")
	upFlags.Bool("no-start", false, "Don't start the services after creating them")
	upFlags.String("pull", "policy", `Pull image before running ("always"|"missing"|"never|policy")`)
	upFlags.Bool("remove-orphans", false, "Remove containers for services not defined in the Compose file")
	upFlags.Int("timeout", 0, "Use this timeout in seconds for container shutdown when attached or when containers are already running")
	upFlags.Bool("timestamps", false, "Show timestamps")
	upFlags.Int("wait-timeout", 0, "Maximum duration in seconds to wait for the project to be running|healthy")
	upFlags.Bool("watch", false, "Watch source code and rebuild/refresh containers when files are updated")
	cfs.AddFlagSet(upFlags)
	c.ComposeUpFlags = upFlags
}

func (c *Config) Load(ctx context.Context) (Transformer, error) {
	options, err := c.toProjectOptions()
	if err != nil {
		return nil, err
	}
	p, err := options.LoadProject(ctx)
	if err != nil {
		return nil, err
	}
	return &transformer{config: c, project: p}, nil
}

func (c *Config) AppendComposeUpFlags(ctx context.Context, opts []string) []string {
	// Need VisitAll here because Visit doesn't use the Changed status of the actual flag, instead
	// it keeps track of flags set in the command's FlagSet.
	c.ComposeUpFlags.VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		fv := f.Value
		dlog.Debugf(ctx, "%s --%s %s", fv.Type(), f.Name, fv.String())
		switch fv.Type() {
		case "bool":
			if fv.String() == "true" {
				opts = append(opts, "--"+f.Name)
			}
		case "stringArray":
			sv := fv.(pflag.SliceValue)
			opt := "--" + f.Name
			for _, v := range sv.GetSlice() {
				opts = append(opts, opt, v)
			}
		default:
			opts = append(opts, "--"+f.Name, fv.String())
		}
	})
	return opts
}

func (c *Config) Run(cmd *cobra.Command, services []string) (err error) {
	defer func() {
		err = errcat.NoDaemonLogs.New(err)
	}()

	c.Services = services
	c.Progress = cmd.Flag(global.FlagProgress).Value.String()
	ctx := cmd.Context()
	w := progress.NewWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), progress.Mode(c.Progress))

	// Tell underlying framework to keep quiet
	ctx = progress.WithContextWriter(ctx, w)
	defer func() {
		progress.Stop(ctx)
	}()

	p, err := c.Load(ctx)
	if err != nil {
		return err
	}
	startTime := time.Now()

	es, err := p.Engagements()
	if err != nil {
		return err
	}

	dlog.Debugf(ctx, "Found %d engagements", len(es))
	if len(es) == 0 {
		return p.RunProject(ctx, nil, startTime)
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	aesCh := make(chan *ActiveEngagement, len(es))
	wg := &sync.WaitGroup{}
	wg.Add(len(es))

	once := sync.Once{}
	latch := make(chan struct{})
	progress.Start(ctx, "Connecting")
	for _, e := range es {
		g.Go(e.Name, func(ctx context.Context) error {
			// We take care of our own cancellation when this goroutine is done.
			// That will happen when ae.done is closed in the defer function in
			// the "compose" routing.
			ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			defer cancel()

			ctx, err := e.Connect(ctx)
			wg.Done()
			if err != nil {
				if errcat.GetCategory(err) == errcat.Silent {
					return err
				}
				return progress.MaybeWriteError(ctx, e.ComposeService, err)
			}

			// Wait for everyone to connect
			wg.Wait()

			// Then start a new progress group
			once.Do(func() {
				progress.Start(ctx, "Engaging")
				close(latch)
			})

			// Wait for the new progress group to be started
			<-latch

			progress.Write(ctx, progress.WorkingEvent(e.ComposeService, e.Type.Working()))
			ae, err := e.Activate(ctx)
			if err != nil {
				if errcat.GetCategory(err) == errcat.Silent {
					return err
				}
				return progress.MaybeWriteError(ctx, e.ComposeService, err)
			}
			progress.Write(ctx, progress.DoneEvent(e.ComposeService, e.Type.WorkDone()))
			aesCh <- ae
			return ae.Wait(ctx)
		})
	}
	g.Go("compose", func(ctx context.Context) error {
		aes := make(map[string]*ActiveEngagement, len(es))
		for len(aes) < len(es) {
			select {
			case <-ctx.Done():
				for _, ae := range aes {
					close(ae.done)
				}
				return nil
			case ae := <-aesCh:
				aes[ae.ComposeService] = ae
			}
		}
		defer func() {
			w.Start(ctx, "Disengaging")
			for _, ae := range aes {
				close(ae.done)
			}
		}()
		progress.Stop(ctx)
		return p.RunProject(ctx, aes, startTime)
	})
	return g.Wait()
}
