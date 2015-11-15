FROM debian
RUN apt-get update && apt-get install -y netcat-traditional
EXPOSE 8000
CMD ["sh", "-c", "while : ; do nc -l -p 8000 -c 'printf \"HTTP/1.0 200 OK\r\n\r\nHello from %s\r\n\" $(hostname)'; done"]
