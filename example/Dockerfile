FROM alpine:3.8

RUN apk --no-cache add netcat-openbsd

EXPOSE 8000
CMD ["sh", "-c", "while : ; do printf 'HTTP/1.0 200 OK\r\n\r\nHello from %s\r\n' $(hostname) | nc -N -l 8000; done"]
