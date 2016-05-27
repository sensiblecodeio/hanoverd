FROM scratch

EXPOSE 8000
ENTRYPOINT ["/bin/busybox", "-c", "while : ; do printf 'HTTP/1.0 200 OK\r\n\r\nHello world from %s\r\n' $(hostname) | nc -l 8000; done"]

COPY . /
