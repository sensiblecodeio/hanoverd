package util

import (
	"os"

	docker "github.com/fsouza/go-dockerclient"
)

func DockerConnect() (*docker.Client, error) {
	docker_host := os.Getenv("DOCKER_HOST")
	if docker_host == "" {
		docker_host = "unix:///run/docker.sock"
	}

	docker_tls_verify := os.Getenv("DOCKER_TLS_VERIFY") != ""

	var (
		client *docker.Client
		err    error
	)
	if docker_tls_verify {
		docker_cert_path := os.Getenv("DOCKER_CERT_PATH")
		docker_cert := docker_cert_path + "/cert.pem"
		docker_key := docker_cert_path + "/key.pem"
		// TODO there's no environment variable option in docker client for
		// this, it's called -tlscacert in its command line. We'll leave it
		// as the default (no CA, just trust) which boot2docker uses.
		docker_ca := ""
		client, err = docker.NewTLSClient(docker_host, docker_cert, docker_key, docker_ca)
	} else {
		client, err = docker.NewClient(docker_host)
	}
	return client, err
}
