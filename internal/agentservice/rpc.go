package agentservice

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"radar/internal/protocol"
)

func call(ctx context.Context, path, method string, params any, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", 1, method, params}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(encoded) > protocol.AgentMaxFrameBytes {
		return fmt.Errorf("agent request exceeds 1 MiB")
	}
	if _, err := conn.Write(append(encoded, '\n')); err != nil {
		return err
	}
	reader := bufio.NewReader(io.LimitReader(conn, protocol.AgentMaxFrameBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return err
	}
	if len(line) > protocol.AgentMaxFrameBytes {
		return fmt.Errorf("agent response exceeds 1 MiB")
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return err
	}
	if response.JSONRPC != "2.0" || response.ID != 1 {
		return fmt.Errorf("invalid agent RPC response")
	}
	if response.Error != nil {
		return &protocol.AgentRPCError{Code: response.Error.Code, Message: response.Error.Message}
	}
	if target != nil {
		return json.Unmarshal(response.Result, target)
	}
	return nil
}
