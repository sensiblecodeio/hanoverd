// Copyright 2014 The Hanoverd Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
)

// DockerErrorStatus returns the HTTP status code represented by `err` or Status
// OK if no error or 0 if err != nil and is not a docker error.
func DockerErrorStatus(err error) int {
	if err, ok := err.(*docker.Error); ok {
		return err.Status
	}
	if err == nil {
		return http.StatusOK
	}
	return 0
}

func main() {
	log.Println("Handover")

	client, err := docker.NewClient("unix:///run/docker.sock")
	if err != nil {
		panic(err)
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	listener := make(chan *docker.APIEvents)
	client.AddEventListener(listener)
	defer close(listener)
	defer client.RemoveEventListener(listener)

	wg.Add(1)
	go func() {
		defer wg.Done()

		// TODO(pwaller): Unsure if this loop serves any purpose yet. We're
		// probably better off doing synchronization elsewhere.

		for ev := range listener {
			switch ev.Status {
			case "create":
			case "start":
				log.Println("Container started:", ev.ID)
			case "die":
				log.Println("Container finished:", ev.ID)
			default:
				log.Printf("Ev: %#+v", ev)
			}
		}
	}()

	// Fired when we're signalled to exit
	var dying barrier.Barrier
	defer dying.Fall()

	go func() {
		defer dying.Fall()
		// Await Stdin closure
		io.Copy(ioutil.Discard, os.Stdin)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		c := NewContainer(client, "hello")
		defer c.Delete()

		c.Create()

		go func() {
			// Listen for close, and then kill the container
			<-dying.Barrier()
			c.closing.Fall()
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			c.CopyOutput(os.Stdout)
		}()

		c.Start()
		c.Wait()
	}()

	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt)

	select {
	case <-sig:
	case <-dying.Barrier():
	}
}

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
