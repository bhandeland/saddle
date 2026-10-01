package session

import "gitlab.com/nighthawk-oss/saddle/internal/runtime"

// The anchor is deliberately tiny and deliberately idle. It is not a place
// anything runs.
const (
	anchorCPUs   = 1
	anchorMemory = "256m"
)

// AnchorName names the anchor container belonging to a session.
func AnchorName(session string) string { return session + "-anchor" }

// AnchorSpec builds the anchor container for a session.
//
// Apple container puts a network's gateway address on the host only while a
// container on that network is running, and takes it away again when the last
// one stops. Every host-side listener a session needs - the egress proxy, and
// any carry_in.mcp spawn - binds that address, so something on the network has
// to be running before any of them can start, and has to stay running for as
// long as the session might come back. That is the anchor's whole job: it is a
// refcount held open, in the shape of a container.
//
// It runs the profile's own image because that image is already local (the
// session needs it regardless), which costs no second pull and leaves nothing
// extra to keep current. The one requirement this puts on a profile's image is
// that it has `sleep`.
func AnchorSpec(session, image, network string) runtime.Spec {
	return runtime.Spec{
		Name:    AnchorName(session),
		Image:   image,
		Network: network,
		Cmd:     []string{"sleep", "infinity"},
		CPUs:    anchorCPUs,
		Memory:  anchorMemory,
	}
}
