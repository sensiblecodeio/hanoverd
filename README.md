Hanoverd - The docker handover daemon
-------------------------------------

Hanoverd ("Hanover-Dee") is responsible for managing seemless transitions from
one application version to another in two docker containers.

User experience:

* You run one hanoverd per docker application.
* You run it in the directory of a Dockerfile.
* Starting hanoverd causes that `Dockerfile` to be built and the application is
  started.
* Sending hanoverd a signal results in hanoverd rebuilding the container and
  transitioning to the new version.
* When the new version successfully accepts a connection, the new version
  becomes live.
* If the new version fails to accept any connection, the old version will chug
  along happily until a new request comes in to start a new application version.

Method:

* Hanoverd is responsible for listening on externally visible sockets and
  forwards them to the currently active docker application.