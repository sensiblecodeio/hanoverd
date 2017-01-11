package util

import (
	docker "github.com/docker/docker/client"
)

// DockerClient configures a docker client supporting docker API 1.24 (docker >=1.12)
func DockerClient() (*docker.Client, error) {
	client, err := docker.NewEnvClient()
	if err != nil {
		return nil, err
	}
	client.UpdateClientVersion("1.24") // support for docker 1.12.
	return client, err
}
