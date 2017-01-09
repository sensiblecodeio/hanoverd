// Copyright 2014 The Hanoverd Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/codegangsta/cli"
	"github.com/docker/docker/pkg/nat"
	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
	"github.com/sensiblecodeio/hookbot/pkg/listen"

	"github.com/sensiblecodeio/hanoverd/pkg/builder"
	"github.com/sensiblecodeio/hanoverd/pkg/iptables"
	"github.com/sensiblecodeio/hanoverd/pkg/source"
	"github.com/sensiblecodeio/hanoverd/pkg/util"
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

type Options struct {
	env, publish, volumes []string

	containerArgs        []string
	ports                nat.PortSet
	portBindings         nat.PortMap
	statusURI            string
	disableOverlap       bool
	overlapGraceDuration time.Duration
}

type UpdateEvent struct {
	Payload       []byte // input
	OutputStream  io.Writer
	Obtained      barrier.Barrier
	BuildComplete chan<- struct{}
}

// Determine if stdin is connected without blocking
func IsStdinReadable() bool {
	unix.SetNonblock(int(os.Stdin.Fd()), true)
	_, err := os.Stdin.Read([]byte{0})
	unix.SetNonblock(int(os.Stdin.Fd()), false)
	return err != io.EOF
}

func main() {
	app := cli.NewApp()

	app.Name = "hanoverd"
	app.Usage = "handover docker containers"

	// Use `hanoverd version` rather than `hanoverd -v`
	app.HideVersion = true

	// Made by `go generate` populating version.go via `git describe`.
	app.Version = Version

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "disable-overlap",
			Usage: "shut down old container before starting new one",
		},
		cli.StringSliceFlag{
			Name:  "env, e",
			Usage: "environment variables to pass (reads from env if = omitted)",
			Value: &cli.StringSlice{},
		},
		cli.StringSliceFlag{
			Name:  "publish, p",
			Usage: "ports to publish (same syntax as docker)",
			Value: &cli.StringSlice{},
		},
		cli.StringSliceFlag{
			Name:  "volume, v",
			Usage: "Bind mount a volume",
			Value: &cli.StringSlice{},
		},
		cli.StringFlag{
			Name:  "status-uri",
			Usage: "specify URI which returns 200 OK when functioning correctly",
			Value: "/",
		},
		cli.StringFlag{
			Name:   "hookbot",
			Usage:  "url of hookbot websocket endpoint to monitor for updates",
			EnvVar: "HOOKBOT_URL",
		},
		cli.DurationFlag{
			Name:  "overlap-grace-duration",
			Usage: "length of time to wait before killing a superceded container",
			Value: 1 * time.Second,
		},
	}

	app.Action = ActionRun

	app.Commands = []cli.Command{
		{
			Name:   "builder",
			Action: builder.Action,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "listen",
					Usage:  "url of hookbot websocket endpoint to monitor for updates",
					EnvVar: "HOOKBOT_MONITOR_URL",
				},
				cli.StringFlag{
					Name:   "docker-notify",
					Usage:  "url of hookbot pub endpoint to notify on complete build",
					EnvVar: "HOOKBOT_DOCKER_NOTIFY_URL",
				},
			},
		},
		{
			Name:   "version",
			Action: cli.ShowVersion,
		},
	}

	app.RunAndExitOnError()
}

func ActionRun(c *cli.Context) {
	var err error

	options := Options{}
	options.volumes = c.StringSlice("volume")
	options.env = makeEnv(c.StringSlice("env"))
	options.statusURI = c.String("status-uri")
	options.disableOverlap = c.Bool("disable-overlap")
	options.overlapGraceDuration = c.Duration("overlap-grace-duration")

	containerName := "hanoverd"
	var imageSource source.ImageSource

	if c.GlobalString("hookbot") != "" {

		hookbotURL := c.GlobalString("hookbot")
		containerName, imageSource, err = source.GetSourceFromHookbot(hookbotURL)
		if err != nil {
			log.Fatalf("Failed to parse hookbot source: %v", err)
		}

		options.containerArgs = c.Args()

	} else if len(c.Args()) == 0 {
		imageSource = &source.CwdSource{}

	} else {
		first := c.Args().First()
		args := c.Args()[1:]

		if first == "@" {
			// If the first arg is "@", then use the Cwd
			imageSource = &source.CwdSource{}
		} else {
			// The argument is a repository[:tag] to pull and run.
			imageSource = source.DockerPullSourceFromImage(first)
		}
		options.containerArgs = args
	}

	if imageSource == nil {
		log.Fatalf("No image source specified")
	}

	if err := iptables.CheckIPTables(); err != nil {
		log.Fatal("Unable to run `iptables -L`, see README (", err, ")")
	}

	options.ports, options.portBindings, err = nat.ParsePortSpecs(c.StringSlice("publish"))
	if err != nil {
		log.Fatalln("--publish:", err)
	}

	log.Println("Hanoverd")

	var wg sync.WaitGroup
	defer wg.Wait()

	// Fired when we're signalled to exit
	var dying barrier.Barrier
	defer dying.Fall()

	if IsStdinReadable() {
		log.Println("Press CTRL-D to exit")
		go func() {
			defer dying.Fall()
			defer log.Println("Stdin closed, exiting...")

			// Await Stdin closure, don't care about errors
			_, _ = io.Copy(ioutil.Discard, os.Stdin)
		}()
	}

	events := make(chan *UpdateEvent, 1)
	originalEvent := &UpdateEvent{}
	events <- originalEvent

	// SIGHUP handler
	go func() {
		sig := make(chan os.Signal)
		signal.Notify(sig, unix.SIGHUP)
		for value := range sig {
			log.Printf("Received signal %s", value)
			// Resend the original event
			events <- originalEvent
		}
	}()

	// SIGTERM, SIGINT handler
	go func() {
		defer dying.Fall()

		var value os.Signal

		defer log.Printf("Received signal %v", value)

		sig := make(chan os.Signal)
		signal.Notify(sig, unix.SIGTERM, unix.SIGINT)
		value = <-sig
	}()

	if c.GlobalString("hookbot") != "" {
		go MonitorHookbot(c.GlobalString("hookbot"), events)
	}

	go loop(containerName, imageSource, &wg, &dying, options, events)

	<-dying.Barrier()
}

func MonitorHookbot(target string, notify chan<- *UpdateEvent) {
	finish := make(chan struct{})
	header := http.Header{}
	events, errs := listen.RetryingWatch(target, header, finish)

	log.Println("Monitoring hookbot")

	go func() {
		for err := range errs {
			log.Printf("Error in MonitorHookbot: %v", err)
		}
	}()

	for payload := range events {

		log.Printf("Signalled via hookbot")
		// data := map[string]string{}
		// err := json.Unmarshal(ev, &data)
		// if err != nil {
		// 	log.Printf("Failed to parse message: %v", err)
		// 	continue
		// }

		done := make(chan struct{})

		outBound := &UpdateEvent{}
		outBound.BuildComplete = done
		outBound.Payload = payload
		// outBound.Source.Type = GithubRepository
		// outBound.Source.githubURL = "github.com/sensiblecodeio/hookbot"
		// outBound.Source.githubRef = data["SHA"]

		notify <- outBound

		<-outBound.Obtained.Barrier()

		// <-done
		log.Printf("--- Build completed %v ---", "TODO(pwaller): Determine name from payload")
	}
}

// Make an env []string from a list of options specified on the cmdline.
func makeEnv(opts []string) []string {
	var env []string
	for _, envVar := range opts {
		if strings.Contains(envVar, "=") {
			env = append(env, envVar)
		} else {
			env = append(env, fmt.Sprint(envVar, "=", os.Getenv(envVar)))
		}
	}
	return env
}

// Main loop managing the lifecycle of all containers.
func loop(
	containerName string,
	imageSource source.ImageSource,
	wg *sync.WaitGroup,
	dying *barrier.Barrier,
	options Options,
	events <-chan *UpdateEvent,
) {
	client, err := util.DockerConnect()
	if err != nil {
		dying.Fall()
		log.Println("Connecting to Docker failed:", err)
		return
	}

	flips := make(chan *Container)
	go flipper(wg, options, flips)

	var i int
	supercede := func() {}

	for event := range events {

		name := fmt.Sprint(containerName, "-", i)
		i++

		log.Printf("New container starting: %q", name)
		if options.disableOverlap {
			log.Printf("Overlap switched off, killing old")
			flips <- nil
		}

		c := NewContainer(client, name, wg)
		c.Args = options.containerArgs
		c.Env = options.env
		c.Volumes = options.volumes
		c.StatusURI = options.statusURI

		c.Obtained.Forward(&event.Obtained)

		// Global exit should cause container exit
		dying.Forward(&c.Closing)

		// Cancel an existing startup, if there is one.
		supercede()
		supercede = c.Superceded.Fall

		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()

			if imageSource == nil {
				log.Printf("No image source specified")
				c.Failed.Fall()
				return
			}

			status, err := c.Run(imageSource, event.Payload)
			if err != nil {
				log.Println("Container run failed:", strings.TrimSpace(err.Error()))
				return
			}
			log.Println("container", c.Name, "quit, exit status:", status)
		}(c)

		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()

			log.Printf("Awaiting container fate: %q %q", c.Name, c.StatusURI)
			select {
			case <-c.Failed.Barrier():
				log.Println("Container failed before going live:", c.Name)
				c.Closing.Fall()
				return
			case <-c.Superceded.Barrier():
				log.Println("Container superceded before going live:", c.Name)
				c.Closing.Fall()
				return
			case <-c.Closing.Barrier():
				log.Println("Container closed before going live:", c.Name)
				return

			case <-c.Ready.Barrier():
			}

			log.Println("Container going live:", c.Name)

			flips <- c
		}(c)
	}
}

// Manage firewall flips
func flipper(
	wg *sync.WaitGroup,
	options Options,
	newContainers <-chan *Container,
) {
	var live *Container

	for container := range newContainers {
		if container == nil {
			// A nil container is an instruction to just kill the
			// running container. (e.g, for --disable-overlap)
			if live != nil {
				live.Closing.Fall()
			}
			live = nil
			continue
		}

		err := flip(wg, options, container)
		if err != nil {
			container.Failed.Fall()
			// Don't flip the firewall rules if there was a problem.
			continue
		}

		if live != nil {
			go func(live *Container) {
				time.Sleep(options.overlapGraceDuration)
				live.Closing.Fall()
			}(live)
		}

		live = container
	}
}

// Make container receive live traffic
func flip(wg *sync.WaitGroup, options Options, container *Container) error {

	removal := []func() error{}

	defer func() {
		// Block main exit until firewall rule has been removed
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-container.Closing.Barrier()

			for _, remove := range removal {
				err := remove()
				if err != nil {
					log.Printf("flip: removal failed: %v", err)
				}
			}
		}()
	}()

	for internalPort, bindings := range options.portBindings {
		if mappedPort, ok := container.MappedPort(internalPort.Int()); ok {
			for _, binding := range bindings {
				var public int
				_, err := fmt.Sscan(binding.HostPort, &public)
				if err != nil {
					// If no public port specified, use same port as internal port
					public = internalPort.Int()
				}

				ipAddress := container.container.NetworkSettings.IPAddress
				remove, err := iptables.ConfigureRedirect(public, mappedPort, ipAddress, internalPort.Int())
				if err != nil {
					// Firewall rule didn't get applied.
					err := fmt.Errorf(
						"flip: ConfigureRedirect (public: %v, private: %v) failed: %q",
						public, internalPort, err)
					container.err(err)
					return err
				}

				removal = append(removal, remove)
			}
		} else {
			err := fmt.Errorf("Docker image not exposing port %v!", internalPort)
			container.err(err)
			return err
		}
	}

	return nil
}
