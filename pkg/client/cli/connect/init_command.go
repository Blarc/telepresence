package connect

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type cmdInitKey struct{}

func WithCommandInitializer(ctx context.Context, cmdInit func(cmd *cobra.Command) error) context.Context {
	return context.WithValue(ctx, cmdInitKey{}, cmdInit)
}

func InitCommand(cmd *cobra.Command) (err error) {
	cmdInit, ok := cmd.Context().Value(cmdInitKey{}).(func(cmd *cobra.Command) error)
	if !ok {
		panic("no registered command initializer")
	}
	return cmdInit(cmd)
}

func InitProgressWriter(cmd *cobra.Command) {
	ctx := cmd.Context()
	if progress.IsNoOp(ctx) {
		mode := progress.ModeAuto
		if me, ok := dos.LookupEnv(ctx, "TELEPRESENCE_PROGRESS"); ok {
			mode = progress.Mode(me)
		} else if pf := cmd.Flag("progress"); pf != nil && pf.Changed {
			mode = progress.Mode(pf.Value.String())
		} else if pa, ok := cmd.Annotations[ann.Progress]; ok {
			mode = progress.Mode(pa)
		}
		w := progress.NewWriter(dos.Stdout(ctx), dos.Stderr(ctx), mode)
		cmd.SetContext(progress.WithContextWriter(ctx, w))
	}
}

func CommandInitializer(cmd *cobra.Command) (err error) {
	InitProgressWriter(cmd)
	ctx := cmd.Context()
	as := cmd.Annotations

	if v, ok := as[ann.Session]; ok {
		as[ann.UserDaemon] = v
		as[ann.VersionCheck] = ann.Required
	}
	progressStarted := false
	defer func() {
		if progressStarted {
			progress.Stop(ctx)
		}
	}()

	var teleroutePort int

	if v := as[ann.UserDaemon]; v == ann.Optional || v == ann.Required {
		if cr := daemon.GetRequest(ctx); cr == nil {
			if ctx, err = daemon.WithDefaultRequest(ctx, cmd); err != nil {
				return err
			}
			flags.DeprecationIfChanged(cmd, global.FlagDocker, "use telepresence connect to initiate the connection")
			flags.DeprecationIfChanged(cmd, global.FlagContext, "use telepresence connect to initiate the connection")
		}
		progress.Start(ctx, "Connecting")
		progressStarted = true
		if daemon.GetRequest(ctx).Docker {
			fps, err := client.FreePortsTCP(1)
			if err != nil {
				return err
			}
			teleroutePort = fps[0].Port
		}
		ctx, err = EnsureUserDaemon(ctx, v == ann.Required, teleroutePort)
		if err != nil {
			if v == ann.Optional && (errors.Is(err, ErrNoUserDaemon) || errcat.GetCategory(err) == errcat.Config) {
				// This is OK, but further initialization is not possible
				err = nil
			}
			return err
		}
		cmd.SetContext(ctx)
	} else {
		// The rest requires a user daemon
		return nil
	}
	if as[ann.VersionCheck] == ann.Required {
		if err = ensureDaemonVersion(ctx); err != nil {
			return err
		}
	}

	if v := as[ann.Session]; v == ann.Optional || v == ann.Required {
		if !progressStarted {
			progress.Start(ctx, "Connecting")
			progressStarted = true
		}
		ctx, err = EnsureSession(ctx, cmd.UseLine(), v == ann.Required, teleroutePort)
		defer progress.Stop(ctx)
		if err != nil {
			return err
		}
		cmd.SetContext(ctx)
	}
	return nil
}

func GetOptionalSession(cmd *cobra.Command) (context.Context, *daemon.Session, error) {
	cmd.Annotations[ann.Session] = ann.Optional
	err := InitCommand(cmd)
	if err != nil {
		return nil, nil, err
	}
	ctx := cmd.Context()
	return ctx, daemon.GetSession(ctx), nil
}
