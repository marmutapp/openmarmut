package dockerrt

import (
	"github.com/docker/docker/client"
)

// newDockerClient creates a real Docker SDK client from environment.
func newDockerClient() (dockerClient, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}
