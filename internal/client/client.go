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

func CreateTask(socketPath, title string) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-create", TaskMutation: &protocol.TaskMutation{Title: title}})
}

func CompleteTask(socketPath string, taskID int) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-done", TaskMutation: &protocol.TaskMutation{TaskID: taskID}})
}

func ReopenTask(socketPath string, taskID int) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-reopen", TaskMutation: &protocol.TaskMutation{TaskID: taskID}})
}

func SetTaskPriority(socketPath string, taskID int, priority string) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-priority", TaskMutation: &protocol.TaskMutation{TaskID: taskID, Priority: priority}})
}

// CallWithTimeout bounds the entire request, including waiting for the daemon.
func CallWithTimeout(socketPath, method string, timeout time.Duration) (protocol.Response, error) {
	return callRequest(socketPath, protocol.Request{Method: method}, timeout)
}

func CallRequest(socketPath string, req protocol.Request) (protocol.Response, error) {
	return callRequest(socketPath, req, 0)
}

func callRequest(socketPath string, req protocol.Request, timeout time.Duration) (protocol.Response, error) {
	deadline := time.Now().Add(timeout)
	dialTimeout := 500 * time.Millisecond
	if timeout > 0 {
		dialTimeout = min(dialTimeout, timeout)
	}
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return protocol.Response{}, err
	}
	defer conn.Close()
	if timeout > 0 {
		if err := conn.SetDeadline(deadline); err != nil {
			return protocol.Response{}, err
		}
	}

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
