package cmd

import (
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// sendKeysCmd represents the send-keys command
var sendKeysCmd = &cobra.Command{
	Use:   "send-keys [PROCESS] [KEYS]",
	Short: "Send keystroke(s) to an interactive process's stdin",
	Long: `Send keystroke(s) to a running interactive process's stdin.

The process must be configured with is_interactive: true. Control keys can be
expressed with escape sequences, e.g. '\x03' for Ctrl-C, '\r' for Enter.`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		keys := args[1]
		err := getClient().SendProcessKeys(name, keys)
		if err != nil {
			log.Fatal().Err(err).Msgf("failed to send keys to process %s", name)
		}
		fmt.Printf("Keys sent to process %s\n", name)
	},
}

func init() {
	processCmd.AddCommand(sendKeysCmd)
}
