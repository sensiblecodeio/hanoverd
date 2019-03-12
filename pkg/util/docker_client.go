package util

import (
	"github.com/docker/docker/client"
)

// DockerClient configures a docker client.
func DockerClient() (*client.Client, error) {
	return client.NewClientWithOpts(
		client.WithVersion("1.39"),
		client.FromEnv,
	)
}
