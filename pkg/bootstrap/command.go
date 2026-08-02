package bootstrap

import (
	"errors"
	"fmt"
)

type Command string

const (
	CommandServe   Command = "serve"
	CommandMigrate Command = "migrate"
)

// ParseCommand preserves the historical no-argument server invocation and
// accepts one explicit, one-shot operational command.
func ParseCommand(arguments []string) (Command, error) {
	switch len(arguments) {
	case 0:
		return CommandServe, nil
	case 1:
		if arguments[0] == string(CommandMigrate) {
			return CommandMigrate, nil
		}
		return "", fmt.Errorf("unknown command %q", arguments[0])
	default:
		return "", errors.New("at most one command is accepted")
	}
}
