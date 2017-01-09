FROM golang:1.6.1

RUN apt-get update && apt-get install -y upx iptables

COPY ./github-host-key /etc/ssh/ssh_known_hosts

# Turn off cgo so that we end up with totally static binaries
ENV CGO_ENABLED=0

RUN go install -v -a -installsuffix=static std

COPY ./vendor /go/src/github.com/sensiblecodeio/hanoverd/vendor/
COPY ./dependencies /go/src/github.com/sensiblecodeio/hanoverd/dependencies

RUN xargs go install -installsuffix=static -v < /go/src/github.com/sensiblecodeio/hanoverd/dependencies

COPY . /go/src/github.com/sensiblecodeio/hanoverd/

RUN go install -x -v -installsuffix=static \
		github.com/sensiblecodeio/hanoverd && \
	upx /go/bin/hanoverd
