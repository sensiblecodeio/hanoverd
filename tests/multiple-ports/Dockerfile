FROM golang:1.20.4-alpine

CMD ["multiple-ports"]
EXPOSE 80 443

COPY . /go/src/multiple-ports
RUN GO111MODULE=auto go install -v /go/src/multiple-ports
