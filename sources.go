package main

import (
	"fmt"

	"github.com/fsouza/go-dockerclient"
)

type ImageSource interface {
	// Build/pull/fetch a docker image and return its name as a string
	Obtain(*docker.Client) (string, error)
}

type CwdSource struct{}

func (s *CwdSource) Obtain(c *docker.Client) (string, error) {
	// `docker build pwd`
	return "", fmt.Errorf("not implemented: CwdSource.Obtain")
}

type RegistrySource struct {
	ImageName string // `localhost.localdomain:5000/image:tag
}

func (s *RegistrySource) Obtain(c *docker.Client) (string, error) {
	// docker pull s.ImageName
	return "", fmt.Errorf("not implemented: RegistrySource.Obtain")
	return s.ImageName, nil
}

type GithubSource struct {
	User, Repository, Ref string
}

func (s *GithubSource) Obtain(c *docker.Client) (string, error) {
	// Obtain/update local mirror
	// Build
	return "", fmt.Errorf("not implemented: GithubSource.Obtain")
}
