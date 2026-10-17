package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"radar/internal/agentservice"
	"radar/internal/client"
	"radar/internal/protocol"
	"radar/internal/socket"
)

func agentCall(request protocol.AgentRequest) (json.RawMessage, error) {
	path, err := socket.Path()
	if err != nil {
		return nil, err
	}
	if err := ensureDaemonCurrent(path); err != nil {
		return nil, err
	}
	response, err := client.CallRequest(path, protocol.Request{Method: "agent", Agent: &request})
	if err != nil {
		if startErr := startDaemonAndWait(path); startErr != nil {
			return nil, startErr
		}
		response, err = client.CallRequest(path, protocol.Request{Method: "agent", Agent: &request})
	}
	if err != nil {
		return nil, err
	}
	if !response.OK {
		if response.AgentErrorCode != 0 {
			return nil, &protocol.AgentRPCError{Code: response.AgentErrorCode, Message: response.Error}
		}
		return nil, errors.New(response.Error)
	}
	return response.AgentResult, nil
}

func runAgent(args []string) {
	if len(args) == 0 {
		fatal(errors.New("usage: radar agent <add|list|inspect|start|stop|restart|emit|message|action|conversation|logs|token|serve>"))
	}
	op, args := args[0], args[1:]
	flags := outputFlags("radar agent " + op)
	id := flags.String("id", "", "registered agent ID")
	configPath := flags.String("config", "", "JSON configuration file")
	workspace := flags.String("workspace", "", "required registered Radar workspace for agent add")
	eventID := flags.String("event-id", "", "stable delivery identity")
	source := flags.String("source", "cli", "producer identity")
	revision := flags.Int("revision", -1, "presentation revision for this interaction")
	listen := flags.String("listen", "127.0.0.1:7433", "event receiver address")
	_ = parseFlags(flags, args)
	request := protocol.AgentRequest{Operation: op}
	if op == "serve" {
		handler := agentservice.NewIngress(agentservice.Token, func(ctx context.Context, id string, event json.RawMessage) (json.RawMessage, error) {
			return agentCall(protocol.AgentRequest{Operation: "emit", ID: id, Params: event})
		})
		server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 16384}
		fmt.Fprintf(os.Stderr, "Radar event ingress listening on %s\n", *listen)
		if err := server.ListenAndServe(); err != nil {
			fatal(err)
		}
		return
	}
	if op == "list" {
		if flags.NArg() != 0 {
			fatal(errors.New("list accepts no arguments"))
		}
	} else {
		if flags.NArg() < 1 {
			fatal(errors.New("agent ID or module is required"))
		}
		request.ID = flags.Arg(0)
	}
	switch op {
	case "add":
		if *workspace == "" {
			fatal(errors.New("agent add requires --workspace"))
		}
		request.Workspace = *workspace
		entry, err := filepath.Abs(flags.Arg(0))
		if err != nil {
			fatal(err)
		}
		request.Entrypoint = entry
		request.ID = *id
		if request.ID == "" {
			request.ID = strings.TrimSuffix(filepath.Base(entry), filepath.Ext(entry))
		}
		if *configPath != "" {
			data, err := os.ReadFile(*configPath)
			if err != nil {
				fatal(err)
			}
			request.Config = data
		}
	case "emit":
		if *eventID == "" {
			fatal(errors.New("emit requires --event-id; reuse it when retrying a delivery"))
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.AgentMaxFrameBytes+1))
		if err != nil {
			fatal(err)
		}
		if len(data) > protocol.AgentMaxFrameBytes || !json.Valid(data) {
			fatal(errors.New("stdin must contain a JSON payload of at most 1 MiB"))
		}
		request.Params, _ = json.Marshal(map[string]any{"id": *eventID, "source": *source, "payload": json.RawMessage(data)})
	case "message", "action":
		if flags.NArg() < 2 {
			fatal(errors.New("message text or action ID is required"))
		}
		if *eventID == "" {
			bytes := make([]byte, 16)
			if _, err := rand.Read(bytes); err != nil {
				fatal(err)
			}
			*eventID = hex.EncodeToString(bytes)
		}
		if *revision < 0 {
			fatal(errors.New("interactions require --revision from the current agent snapshot"))
		}
		params := map[string]any{"eventId": *eventID, "revision": *revision}
		if op == "message" {
			params["text"] = strings.Join(flags.Args()[1:], " ")
		} else {
			params["action"] = flags.Arg(1)
			if flags.NArg() > 2 {
				params["text"] = strings.Join(flags.Args()[2:], " ")
			}
		}
		request.Operation = "interact"
		request.Params, _ = json.Marshal(params)
	case "conversation":
		if flags.NArg() != 2 {
			fatal(errors.New("conversation requires agent ID and conversation ID"))
		}
		var conversationID int
		if _, err := fmt.Sscanf(flags.Arg(1), "%d", &conversationID); err != nil {
			fatal(err)
		}
		request.Params, _ = json.Marshal(map[string]int{"id": conversationID})
	case "list", "inspect", "start", "stop", "restart", "remove", "logs", "token":
	default:
		fatal(fmt.Errorf("unknown agent operation %q", op))
	}
	data, err := agentCall(request)
	if err != nil {
		fatal(err)
	}
	if jsonOutput {
		fmt.Println(string(data))
		return
	}
	printAgentResult(op, data)
}

func printAgentResult(operation string, data json.RawMessage) {
	switch operation {
	case "list":
		var values []protocol.AgentStatus
		if err := json.Unmarshal(data, &values); err != nil {
			fatal(err)
		}
		if len(values) == 0 {
			fmt.Println("No registered agents")
			return
		}
		for _, value := range values {
			state := "stopped"
			if value.Running {
				state = "running"
			}
			fmt.Printf("%s\t%s\t%s (%s)\n", value.ID, state, value.Workspace.Path, value.Workspace.Mode)
		}
		return
	case "add", "inspect", "start", "stop", "restart":
		var value protocol.AgentStatus
		if err := json.Unmarshal(data, &value); err != nil {
			fatal(err)
		}
		state := "stopped"
		if value.Running {
			state = "running"
		}
		fmt.Printf("%s · %s\nWorkspace: %s (%s)\nDefinition: %s\nData: %s\n", value.ID, state, value.Workspace.Path, value.Workspace.Mode, value.Entrypoint, value.DataDir)
		if value.Error != "" {
			fmt.Println("Runtime:", value.Error)
		}
		if value.Snapshot != nil {
			p := value.Snapshot.Presentation
			fmt.Printf("Revision: %d · pending: %d · attention: %t\n%s\n", p.Revision, value.Snapshot.Pending, p.Attention, p.Summary)
			if value.Snapshot.Problem != "" {
				fmt.Println("Runtime:", value.Snapshot.Problem)
			}
			for _, action := range p.Actions {
				fmt.Printf("Action: %s — %s\n", action.ID, action.Label)
			}
			for _, c := range p.Conversations {
				fmt.Printf("Conversation: %d — %s\n", c.ID, c.Label)
			}
		}
		return
	case "emit", "message", "action":
		var receipt struct {
			TaskID  int    `json:"taskId"`
			Source  string `json:"source"`
			EventID string `json:"eventId"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil {
			fatal(err)
		}
		fmt.Printf("Accepted %s/%s · task %d\n", receipt.Source, receipt.EventID, receipt.TaskID)
		return
	case "logs", "token":
		var values map[string]string
		if err := json.Unmarshal(data, &values); err != nil {
			fatal(err)
		}
		key := "path"
		if operation == "token" {
			key = "token"
		}
		fmt.Println(values[key])
		return
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		fatal(err)
	}
	formatted, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(formatted))
}
