package main

import (
	"fmt"
	"os"

	"github.com/openmcp-project/cluster-provider-k3d/cmd/cluster-provider-k3d/app"
)

func main() {
	cmd := app.NewClusterProviderCommand()

	if err := cmd.Execute(); err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
}
