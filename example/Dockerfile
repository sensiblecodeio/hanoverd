FROM alpine

RUN apk --no-cache add netcat-openbsd

EXPOSE 8000
CMD ["sh", "-c", "while : ; do printf 'HTTP/1.0 200 OK\r\n\r\nHello from %s\r\n' $(hostname)| nc -l 8000; done"]
