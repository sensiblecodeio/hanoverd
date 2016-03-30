FROM golang:1.6

CMD ["multiple-ports"]
EXPOSE 80 443

COPY . /go/src/multiple-ports
RUN go install -v multiple-ports
