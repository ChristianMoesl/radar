package client

import (
	"bufio"
	"encoding/json"
	"net"
	"time"

	"radar/internal/protocol"
)

func Call(socketPath string, method string) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: method})
}

func CallRequest(socketPath string, req protocol.Request) (protocol.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return protocol.Response{}, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return protocol.Response{}, err
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return protocol.Response{}, err
	}

	var res protocol.Response
	if err := json.Unmarshal(line, &res); err != nil {
		return protocol.Response{}, err
	}
	return res, nil
}
