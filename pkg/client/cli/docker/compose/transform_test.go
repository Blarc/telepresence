package compose

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

func loadSpec(ctx context.Context, c *Config, spec string) (Transformer, error) {
	saveIn := os.Stdin
	r, w, _ := os.Pipe()
	defer func() {
		os.Stdin = saveIn
	}()
	_, _ = w.WriteString(spec)
	w.Close()
	os.Stdin = r
	return c.Load(ctx)
}

func testContext(t *testing.T) context.Context {
	ctx := dlog.NewTestContext(t, false)
	env, err := client.LoadEnv()
	require.NoError(t, err)
	ctx = client.WithEnv(ctx, env)
	cfg, err := client.LoadConfig(ctx)
	require.NoError(t, err)
	return client.WithConfig(ctx, cfg)
}

func TestLoad(t *testing.T) {
	c := Config{
		ConfigPaths: []string{"-"},
	}

	ctx := testContext(t)
	tr, err := loadSpec(ctx, &c, `
services:
  my-service:
    x-telepresence:
      workload: echo
      type: replace
      ports:
      - 80:80
    image: ghcr.io/telepresenceio/echo-server
`)
	require.NoError(t, err)

	es, err := tr.Engagements()
	require.NoError(t, err)
	require.Len(t, es, 1)
	e, ok := es["my-service"]
	require.True(t, ok)
	require.Equal(t, "echo", e.Extension.Workload)
}

func TestBadType(t *testing.T) {
	c := Config{
		ConfigPaths: []string{"-"},
	}
	ctx := testContext(t)
	tr, err := loadSpec(ctx, &c, `
services:
  my-service:
    x-telepresence:
      workload: echo
      type: badType
    image: ghcr.io/telepresenceio/echo-server
`)
	require.NoError(t, err)
	_, err = tr.Engagements()
	require.ErrorContains(t, err, `invalid engagement type: "badType"`)
}

func TestBadPortMapping(t *testing.T) {
	c := Config{
		ConfigPaths: []string{"-"},
	}
	ctx := testContext(t)
	tr, err := loadSpec(ctx, &c, `
services:
  my-service:
    x-telepresence:
      workload: echo
      type: intercept
      ports:
      - badPort:nope
    image: ghcr.io/telepresenceio/echo-server
`)
	require.NoError(t, err)
	_, err = tr.Engagements()
	require.ErrorContains(t, err, `"/ports/0": not an integer`)
}

func TestApply(t *testing.T) {
	c := Config{
		ConfigPaths: []string{"-"},
	}
	ctx := testContext(t)
	tr, err := loadSpec(ctx, &c, `
services:
  svc1:
    x-telepresence:
      workload: echo
      type: replace
      namespace: alpha
      ports:
      - 80:80
    image: ghcr.io/telepresenceio/echo-server
    volumes:
      - one:/var/one
      - /var/two:/var/two
  svc2:
    x-telepresence:
      workload: echo2
      type: replace
      namespace: alpha
      ports:
      - 80:80
    image: ghcr.io/telepresenceio/echo-server
volumes:
  one:
`)
	require.NoError(t, err)

	es, err := tr.Engagements()
	require.NoError(t, err)
	require.Len(t, es, 2)
	di1, err := daemon.NewIdentifier("svc1", "default", "default", true)
	require.NoError(t, err)
	di2, err := daemon.NewIdentifier("svc2", "default", "default", true)
	require.NoError(t, err)
	aes := map[string]*ActiveEngagement{
		"svc1": {
			Engagement: es["svc1"],
			DaemonID:   di1,
			Environment: map[string]string{
				"HELLO": "WORLD",
			},
		},
		"svc2": {
			Engagement: es["svc2"],
			DaemonID:   di2,
			Environment: map[string]string{
				"HI": "PLANET",
			},
		},
	}
	require.NoError(t, tr.ApplyEngagements(aes))
	y, err := tr.MarshalYAML()
	require.NoError(t, err)
	t.Log(string(y))
}
