package egress

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const connectArg = "__xhaos_connect"

// ConnectCommand is the ssh ProxyCommand for a sealed command: the server's
// own binary, as a small client that tunnels stdin and stdout through the
// proxy (CONNECT host:port with the lease token from XHAOS_PROXY).
func ConnectCommand(self string) string { return fmt.Sprintf("%q %s %%h %%p", self, connectArg) }

// MaybeConnect runs the tunnel when this process was started as ssh's
// ProxyCommand; it never returns then. Call it first in main.
func MaybeConnect() {
	if len(os.Args) != 4 || os.Args[1] != connectArg {
		return
	}
	if err := connect(os.Getenv("XHAOS_PROXY"), net.JoinHostPort(os.Args[2], os.Args[3]), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "xhaos connect: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func connect(proxyURL, target string, in io.Reader, out io.Writer) error {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" || u.User == nil {
		return fmt.Errorf("no proxy for this command (XHAOS_PROXY)")
	}
	conn, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	pass, _ := u.User.Password()
	auth := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pass))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("%s: %s", res.Status, body)
	}
	// Send until stdin ends (then half-close), and run until the remote
	// side closes: ssh closes stdin only once it is done.
	go func() { io.Copy(conn, in); conn.(*net.TCPConn).CloseWrite() }()
	_, err = io.Copy(out, br)
	return err
}
