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
	defer close(listener)
	client.AddEventListener(listener)
	defer client.RemoveEventListener(listener)

	wg.Add(1)
	go func() {
		defer wg.Done()

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
	go start(client, "hello", &wg, dying.Barrier())

	sig := make(chan os.Signal)
	signal.Notify(sig)

	select {
	case <-sig:
	case <-dying.Barrier():
	}
}

func start(client *docker.Client, name string, wg *sync.WaitGroup, dying <-chan struct{}) {
	defer wg.Done()

	opts := docker.CreateContainerOptions{
		Name: name,
		Config: &docker.Config{
			Image: "base",
			// Cmd:          []string{"date"},
			Cmd: []string{"bash", "-c", onelineweb},

			Hostname:     "hello",
			ExposedPorts: map[docker.Port]struct{}{"8000/tcp": struct{}{}},
			AttachStdout: true,
			AttachStderr: true,
		},
	}

	container, err := client.CreateContainer(opts)
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

	defer func() {
		// Delete the container
		defer log.Println("Container deleted")
		client.RemoveContainer(docker.RemoveContainerOptions{
			ID:            name,
			RemoveVolumes: true,
			Force:         true,
		})
	}()

	// reader, writer := io.Pipe()
	// wg.Add(1)
	// go func() {
	// 	defer wg.Done()
	// 	io.Copy(os.Stderr, reader)
	// }()

	wg.Add(1)
	go func() {
		defer wg.Done()
		// defer writer.Close()
		// Blocks until stream closed
		err = client.AttachToContainer(docker.AttachToContainerOptions{
			Container: container.ID,
			// OutputStream: writer,
			// ErrorStream:  writer,
			OutputStream: os.Stderr,
			ErrorStream:  os.Stderr,
			Logs:         true,
			Stdout:       true,
			Stderr:       true,
			Stream:       true,
		})
		if err != nil {
			log.Fatal(err)
		}
	}()

	hc := &docker.HostConfig{
		// Bind mounts
		// Binds: []string{""},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"8000/tcp": []docker.PortBinding{
				docker.PortBinding{HostIP: "0.0.0.0"},
			},
		},
	}
	err = client.StartContainer(container.ID, hc)
	if err != nil {
		panic(err)
	}

	// Load container.NetworkSettings
	container, err = client.InspectContainer(container.ID)
	if err != nil {
		panic(err)
	}

	for _, port := range container.NetworkSettings.PortMappingAPI() {
		log.Println("port:", port)
	}

	go func() {
		// Listen for close, and then kill the container
		<-dying
		client.KillContainer(docker.KillContainerOptions{
			ID: container.ID,
		})
	}()

	w, err := client.WaitContainer(container.ID)
	if err != nil {
		panic(err)
	}

	log.Println("Container exited:", w)
}

const onelineweb = "while true; do { echo -e 'HTTP/1.1 200 OK\r\n'; echo hello world; } | nc -l 8000; done"
