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

	c := NewContainer(client, "hello", &wg)

	go func() {
		// Listen for close, and then kill the container
		<-dying.Barrier()
		c.Closing.Fall()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Run()
	}()

	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt)

	select {
	case <-sig:
	case <-dying.Barrier():
	}
}
