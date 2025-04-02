package flags

import (
	"context"

	"github.com/spf13/pflag"
)

type flagSetsKey struct{}

func WithFlagSets(ctx context.Context, flagSets ...*pflag.FlagSet) context.Context {
	if len(flagSets) > 0 {
		ctx = context.WithValue(ctx, flagSetsKey{}, flagSets)
	}
	return ctx
}

func GetFlagSets(ctx context.Context) []*pflag.FlagSet {
	if fs, ok := ctx.Value(flagSetsKey{}).([]*pflag.FlagSet); ok {
		return fs
	}
	return nil
}
