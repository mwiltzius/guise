// Package mount serves a guise filesystem with the platform's backend:
// NFS on macOS (no driver needed), FUSE on Linux.
package mount

// Server is a running mount.
type Server interface {
	// Stop unmounts and stops serving.
	Stop() error
	// Done receives when the server stops on its own.
	Done() <-chan error
}
