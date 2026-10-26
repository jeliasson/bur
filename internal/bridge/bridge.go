// Package bridge serves the host side of a bur bridge socket. The
// clipboard and package bridges both listen on a unix socket in the env
// dir, mounted into the sandbox at /run/bur, and differ only in the
// protocol they speak over an accepted connection.
package bridge

import (
	"net"
	"os"
)

// Listen accepts connections on a unix socket at path and hands each to
// handle in its own goroutine, until the process exits.
func Listen(path string, handle func(net.Conn)) error {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// keep-id maps the sandbox to the same uid, so owner-only suffices.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return nil
}
