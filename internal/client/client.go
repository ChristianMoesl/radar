package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
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

func MuteTask(socketPath string, taskID int) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-mute", TaskMutation: &protocol.TaskMutation{TaskID: taskID}})
}

func UnmuteTask(socketPath string, taskID int) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-unmute", TaskMutation: &protocol.TaskMutation{TaskID: taskID}})
}

func SetTaskPriority(socketPath string, taskID int, priority string) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-priority", TaskMutation: &protocol.TaskMutation{TaskID: taskID, Priority: priority}})
}

func PreviewTaskDeletion(socketPath string, taskID int) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-delete-preview", TaskID: taskID})
}

func DeleteTask(socketPath string, preview protocol.TaskDeletionPreview) (protocol.Response, error) {
	return CallRequest(socketPath, protocol.Request{Method: "task-delete", TaskDeletion: &preview})
}

// CallWithTimeout bounds the entire request, including waiting for the daemon.
func CallWithTimeout(socketPath, method string, timeout time.Duration) (protocol.Response, error) {
	return callRequest(socketPath, protocol.Request{Method: method}, timeout)
}

// ReportResourceMutation acknowledges completed or possibly partial local
// changes. It never asks the daemon to execute or replay the reported action.
func ReportResourceMutation(socketPath string, target protocol.ResourceMutation) (protocol.Response, error) {
	return callRequest(socketPath, protocol.Request{Method: "resources-mutated", ResourceMutation: &target}, time.Second)
}

// MaintainDiagnostics uses only the dedicated typed maintenance RPC. It never
// starts the daemon, retries an ambiguous apply, or invokes ordinary refresh/GC.
func MaintainDiagnostics(socketPath string, req protocol.DiagnosticMaintenanceRequest, timeout time.Duration) (protocol.Response, error) {
	return callRequest(socketPath, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req}, timeout)
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

	cleanup := req.Method == "diagnostics-maintenance" && req.Maintenance != nil && req.Maintenance.Operation == "cleanup"
	retirement := req.Method == "diagnostics-maintenance" && req.Maintenance != nil && req.Maintenance.Operation == "retire"
	recreation := req.Method == "diagnostics-maintenance" && req.Maintenance != nil && req.Maintenance.Operation == "recreate"
	var reader io.Reader = conn
	if cleanup || recreation || retirement {
		reader = io.LimitReader(conn, 36*1024+1)
	}
	line, err := bufio.NewReader(reader).ReadBytes('\n')
	if err != nil {
		return protocol.Response{}, err
	}

	if retirement {
		response, err := decodeDiagnosticRetirement(line)
		if err == nil && response.OK && (response.DiagnosticRetirement == nil || !protocol.ValidDiagnosticRetirement(*response.DiagnosticRetirement, *req.Maintenance)) {
			return protocol.Response{}, errors.New("native retirement response does not match exact request; outcome may be unknown, do not replay")
		}
		return response, err
	}
	if recreation {
		response, err := decodeDiagnosticRecreation(line)
		if err == nil && response.OK && (response.DiagnosticRecreation == nil || !protocol.ValidDiagnosticRecreation(*response.DiagnosticRecreation, *req.Maintenance)) {
			return protocol.Response{}, errors.New("native recreation response does not match exact request; outcome may be unknown, do not replay")
		}
		return response, err
	}
	if cleanup {
		response, err := decodeDiagnosticCleanup(line)
		if err == nil && response.OK && (response.DiagnosticCleanup == nil || !protocol.ValidDiagnosticCleanup(*response.DiagnosticCleanup, *req.Maintenance)) {
			return protocol.Response{}, errors.New("native manual cleanup response does not match exact request; outcome may be unknown, do not replay")
		}
		return response, err
	}
	var res protocol.Response
	if err := json.Unmarshal(line, &res); err != nil {
		return protocol.Response{}, err
	}
	return res, nil
}

// ProbeDiagnostics explicitly requests a bounded registered runtime observation.
// It never starts the daemon or falls back to a general command endpoint.
func ProbeDiagnostics(socketPath string, request protocol.DiagnosticsRequest, timeout time.Duration) (protocol.Response, error) {
	return callRequest(socketPath, protocol.Request{Method: "diagnostics-probe", Diagnostics: &request}, timeout)
}

// InspectDiagnostics reads cached state/registration or the fixed daemon-process
// configuration allowlist for overview, without daemon startup or provider calls.
func InspectDiagnostics(socketPath string, request protocol.DiagnosticsRequest, timeout time.Duration) (protocol.Response, error) {
	return callRequest(socketPath, protocol.Request{Method: "diagnostics", Diagnostics: &request}, timeout)
}
