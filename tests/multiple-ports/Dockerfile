FROM golang:1.14.4-alpine

CMD ["multiple-ports"]
EXPOSE 80 443

COPY . /go/src/multiple-ports
RUN go install -v multiple-ports
