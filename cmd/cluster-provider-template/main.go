//go:generate opencontrolplane-gen
package main

import (
	"fmt"
	"os"

	// opencontrolplane-gen:replace github.com/openmcp-project/cluster-provider-template=MODULE cluster-provider-template=CMD_FOLDER
	"github.com/openmcp-project/cluster-provider-template/cmd/cluster-provider-template/app"
)

func main() {
	cmd := app.NewClusterProviderCommand()

	if err := cmd.Execute(); err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
}
