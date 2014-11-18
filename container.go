package main

import (
	"io"
	"log"
	"net/http"

	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
)

type Container struct {
	Name string

	client    *docker.Client
	container *docker.Container
	closing   barrier.Barrier
}

func NewContainer(client *docker.Client, name string) *Container {
	return &Container{
		Name:   name,
		client: client,
	}
}

func (c *Container) Create() {
	opts := docker.CreateContainerOptions{
		Name: c.Name,
		Config: &docker.Config{
			Image: "base",
			// Cmd:          []string{"date"},
			Cmd: []string{"bash", "-c", onelineweb},

			Hostname:     "container",
			ExposedPorts: map[docker.Port]struct{}{"8000/tcp": struct{}{}},
			AttachStdout: true,
			AttachStderr: true,
		},
	}

	var err error
	c.container, err = c.client.CreateContainer(opts)
	switch DockerErrorStatus(err) {
	default:
		fallthrough
	case 0:
		panic(err)
	case http.StatusConflict:
		log.Fatalln("Container already exists. Aborting.")
	case http.StatusOK:
		break
	}
}

// CopyOutput copies the output of the container to `w` and blocks until
// completion
func (c *Container) CopyOutput(w io.Writer) {
	// Blocks until stream closed
	err := c.client.AttachToContainer(docker.AttachToContainerOptions{
		Container:    c.container.ID,
		OutputStream: w,
		ErrorStream:  w,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})
	if err != nil {
		log.Fatal(err)
	}
}

func (c *Container) AwaitListening() {
	var err error
	// Load container.NetworkSettings
	c.container, err = c.client.InspectContainer(c.container.ID)
	if err != nil {
		panic(err)
	}

	// TODO(pwaller): Listening logic
	for _, port := range c.container.NetworkSettings.PortMappingAPI() {
		log.Println("port:", port)
	}
}

func (c *Container) Start() {
	hc := &docker.HostConfig{
		// Bind mounts
		// Binds: []string{""},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"8000/tcp": []docker.PortBinding{
				docker.PortBinding{HostIP: "0.0.0.0"},
			},
		},
	}
	err := c.client.StartContainer(c.container.ID, hc)
	if err != nil {
		panic(err)
	}
}

func (c *Container) Wait() {
	go func() {
		<-c.closing.Barrier()
		// If the container is signaled to close, send a kill signal
		c.client.KillContainer(docker.KillContainerOptions{
			ID: c.container.ID,
		})
	}()

	w, err := c.client.WaitContainer(c.container.ID)
	if err != nil {
		panic(err)
	}

	log.Println("Exit status:", w)
}

func (c *Container) Delete() {
	c.client.RemoveContainer(docker.RemoveContainerOptions{
		ID:            c.container.ID,
		RemoveVolumes: true,
		Force:         true,
	})
}

const onelineweb = "while true; do { echo -e 'HTTP/1.1 200 OK\r\n'; echo hello world; } | nc -l 8000; done"
