package main

import (
	"os"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/onukura/simple-json-backend-datasource/pkg/plugin"
)

func main() {
	ds := plugin.NewJsonDatasource()

	opts := datasource.ServeOpts{
		QueryDataHandler:   ds,
		CheckHealthHandler: ds,
	}

	if err := datasource.Serve(opts); err != nil {
		backend.Logger.Error(err.Error())
		os.Exit(1)
	}

}
