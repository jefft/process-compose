package health

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/rs/zerolog/log"
)

type execChecker struct {
	name        string
	command     string
	timeout     int
	workingDir  string
	env         []string
	shellConfig command.ShellConfig
	logCmdOnce  sync.Once
}

func (c *execChecker) Status() (any, error) {
	c.logCmdOnce.Do(func() {
		log.Debug().Msgf("%s executing probe command: %s", c.name, c.command)
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.timeout)*time.Second)
	defer cancel()

	cmd := command.BuildCommandShellArgContext(ctx, c.shellConfig, c.command)
	cmd.SetDir(c.workingDir)
	cmd.SetEnv(c.env)

	rcMap := make(map[string]string)
	out, err := cmd.CombinedOutput()
	if err != nil {
		rcMap["error"] = err.Error()
	}
	rcMap["output"] = string(out)
	rcMap["exit_code"] = strconv.Itoa(cmd.ExitCode())
	rcMap["command"] = c.command
	return rcMap, err
}
