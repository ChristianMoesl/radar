package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"radar/internal/protocol"
)

func runAgentSchedule(args []string) {
	if len(args) == 0 {
		fatal(errors.New("usage: radar agent schedule <set|pause|resume|remove> <agent-id> <name>"))
	}
	op := args[0]
	flags := outputFlags("radar agent schedule " + op)
	clock := flags.String("time", "", "local time (HH:MM)")
	weekdays := flags.String("weekdays", "", "selected weekdays, e.g. mon,tue,wed,thu,fri")
	zone := flags.String("timezone", "", "explicit IANA timezone, e.g. Europe/Vienna")
	payload := flags.String("payload", "", "definition-owned JSON payload file")
	_ = parseFlags(flags, args[1:])
	if flags.NArg() != 2 {
		fatal(errors.New("schedule requires agent ID and schedule name"))
	}
	config := protocol.AgentScheduleConfig{Name: flags.Arg(1)}
	switch op {
	case "set":
		if *clock == "" || *weekdays == "" || *zone == "" || *payload == "" {
			fatal(errors.New("schedule set requires --time, --weekdays, --timezone and --payload"))
		}
		f, err := os.Open(*payload)
		if err != nil {
			fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(f, protocol.AgentMaxFrameBytes+1))
		_ = f.Close()
		if err != nil {
			fatal(err)
		}
		if len(data) > protocol.AgentMaxFrameBytes || !json.Valid(data) {
			fatal(errors.New("schedule payload must contain JSON within the admission limit"))
		}
		config.Time, config.Weekdays, config.Timezone, config.Payload = *clock, strings.Split(*weekdays, ","), *zone, data
	case "pause", "resume", "remove":
		if *clock != "" || *weekdays != "" || *zone != "" || *payload != "" {
			fatal(errors.New("schedule configuration flags apply only to set"))
		}
	default:
		fatal(fmt.Errorf("unknown schedule operation %q", op))
	}
	params, _ := json.Marshal(config)
	data, err := agentCall(protocol.AgentRequest{Operation: "schedule." + op, ID: flags.Arg(0), Params: params})
	if err != nil {
		fatal(err)
	}
	if jsonOutput {
		fmt.Println(string(data))
		return
	}
	printAgentResult("inspect", data)
}
