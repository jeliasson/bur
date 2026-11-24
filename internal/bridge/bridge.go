// Package bridge serves the host side of a bur bridge socket: the clipboard
// and package bridges both listen in the env dir, mounted into the sandbox at
// /run/bur, and differ only in the protocol they speak over a connection.
package bridge

import (
	"net"
	"os"
)

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
