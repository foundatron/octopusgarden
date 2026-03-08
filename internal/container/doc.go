// Package container manages Docker image builds, container lifecycle, and
// health checking for generated code execution.
//
// Manager wraps the Docker API to build images from a source directory
// (assembled as an in-memory tarball), run containers with port bindings, and
// poll HTTP or TCP endpoints until they are healthy. Long-lived exec sessions
// are available via StartSession and Session.Exec for scenario steps that run
// shell commands inside a running container. Close releases the underlying
// Docker client connection.
package container
