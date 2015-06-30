FROM golang:1.4

RUN apt-get update && apt-get install -y upx iptables

COPY ./github-host-key /etc/ssh/ssh_known_hosts

RUN go get github.com/pwaller/goupx

# Turn off cgo so that we end up with totally static binaries
ENV CGO_ENABLED 0

RUN go install -a -installsuffix=static std

COPY ./vendor /go/src/

RUN go install \
	-v -installsuffix=static \
	github.com/codegangsta/cli \
	github.com/docker/docker/nat \
	github.com/docker/docker/pkg/jsonmessage \
	github.com/fsouza/go-dockerclient \
	github.com/pwaller/barrier \
	github.com/scraperwiki/hookbot/pkg/listen \
	github.com/vaughan0/go-ini \
	golang.org/x/net/context \
	golang.org/x/sys/unix

COPY . /go/src/github.com/scraperwiki/hanoverd/

RUN go install -x -v -installsuffix=static \
		github.com/scraperwiki/hanoverd && \
	goupx /go/bin/hanoverd
