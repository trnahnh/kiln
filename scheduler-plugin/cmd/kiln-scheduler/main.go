// kiln-scheduler is kube-scheduler with the CostAware plugin compiled in, run as a second
// scheduler (schedulerName: kiln-scheduler) alongside the default one.
package main

import (
	"os"

	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/plugin"
)

func main() {
	command := app.NewSchedulerCommand(app.WithPlugin(plugin.Name, plugin.New))
	os.Exit(cli.Run(command))
}
