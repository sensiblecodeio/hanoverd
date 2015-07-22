Hanoverd - The docker handover daemon
-------------------------------------

Hanoverd ("Hanover-Dee") is responsible for managing seamless transitions
from one application version to another with Docker containers.

It's the [`docker replace`](https://github.com/docker/docker/issues/2733#issuecomment-123502548) command you always wanted.

Status: beta. Things may change. We may need your feedback to make it work for you.

## What is hanoverd good for?

Let's say you have your web application running in docker, and you
want to upgrade it seemlessly.

You want to move fast and break things with continuous deployment,
making updates to your app, but you don't want your web application
to go down while it updates; or if it fails to restart.

Hanoverd solves the problem by leaving the first working version
running until a second one has started and correctly accepted a
request.

Hanoverd is ideal for stateless web applications running in docker
where you can easily have multiple instances running side-by-side.

Hanoverd can pull or build images from a variety of sources and
also listen to webhooks from github and elsewhere using an outbound
TCP connection, rather than having to expose a port for listening to
such events. This is done via [hookbot](#hookbot).

## Installation

Hanoverd currently requires the ability to run iptables. This can be
achieved with setcap to avoid using root. A suitably capable `iptables`
command must be on the `$PATH`. This can be achieved with this command,
for example:

```
cp $(which iptables) .
sudo setcap 'cap_net_admin,cap_net_raw=+ep' iptables
PATH=.:$PATH hanoverd
```

(or you can use some directory other than `.`).

You should bear in mind that the ability to run iptables is the ability
to do almost arbitrary things to the network. However, hanoverd can
run as a separate user from the container, so it does not share this
privilege.

## User experience

* You run one hanoverd per application you wish to run in Docker.
* You run hanoverd in a directory containing a `Dockerfile`.
* Starting hanoverd causes that `Dockerfile` to be built and the application is
  started.
* Sending hanoverd a signal results in hanoverd rebuilding the container and
  transitioning to the new version.
* When the new version successfully accepts a connection, the new version
  becomes live.
* If the new version fails to accept any connection, the old version will chug
  along happily until a new request comes in to start a new application version.

## Building and running

First build it:

```
$ go get -v
$ go build -v
```

Then launch it (in a directory with a Dockerfile!):

```
$ ./hanoverd
```

Supported command line options are the same sort of things as
docker. However not all are implemented -
[please submit an issue](https://github.com/scraperwiki/hanoverd/issues/new)
if there is one you need which is missing.

* `--env`, `-e` for environment, e.g. `--env HOME` to pass `$HOME` through or `--env HOME=/home/foo`
* `--publish`, `-p` for specifying port mappings
* `--volume`, `-v` for volumes

Other things:

* `--status-uri` (defaults to `/`), specify a status URL to send HTTP pings to to determine initial health
* `--hookbot`, specify a hookbot websocket URL to listen on

Environment variables which the docker client (and boot2docker) use
can be set first.

    DOCKER_CERT_PATH
    DOCKER_HOST
    DOCKER_TLS_VERIFY

## Method

Hanoverd has a few phases. It commences this when it first starts or
when it is signalled.
 
* Obtain image (can be done a few ways)
* Start container with the new image
* Rapidly poll container with requests until it gives a 200 response
* Redirect traffic to the new container using iptables rules
* Stop the old container
* Delete the old image

If a signal comes in to start a new container and the previous
one has not had new traffic directed to it yet, image obtaining is
cancelled or the container is stopped and deleted.

## Obtaining an image

Images can be obtained via building them or pulling them from a
registry. This part is extensible, so coding something to import
a tar from S3 would also be doable, for example.

## Hookbot

[Hookbot](https://github.com/scraperwiki/hookbot) is a service
that allows applications such as hanoverd to listen for webhooks
by using an *outbound* websocket.

Hanoverd can make an outbound connection to a hookbot server in order
to respond to webhook events without needing to listen for them.
Hanoverd supports a few different URL types, for example to build
and run a github project on a specific branch whenever the branch changes:

```
hanoverd --hookbot wss://TOKEN@hookbot.scraperwiki.com/sub/github.com/repo/scraperwiki/project/branch/master
```

Or to pull from a docker registry:

```
hanoverd --hookbot wss://TOKEN@hookbot.scraperwiki.com/sub/docker-pull/localhost.localdomain:5000/pdftables.com/tag/master
```

## Plans (not a promise, may never happen)

Right now restarts are not-quite-zero-downtime. We haven't seen
a broken connection because of it, but if you have long running
requests it could happen. This could be reasonably easily fixed
by giving the old container some grace time.
