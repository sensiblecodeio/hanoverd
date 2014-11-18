# This project is in the early prototype / dreaming phase

Hanoverd - The docker handover daemon
-------------------------------------

Hanoverd ("Hanover-Dee") is responsible for managing seemless transitions from
one application version to another with Docker containers.

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

## Method

* Hanoverd is responsible for listening on externally visible sockets and
  forwards them to the currently active docker application.
* Hanoverd can be signalled via POSIX signals or via a HTTP ping.

## Plans

* Hanoverd is not currently responsible for receiving a hook to run updates -
  that may be the responsibility of another application
* Hanoverd can provide log messages live via a web interface.
* "Power on self test" allows hanoverd to also run some tests before declaring
  the new container ready to go live.