package cmd

import (
	"fmt"

	"github.com/netlify/gocommerce/api"
	"github.com/netlify/gocommerce/conf"
	"github.com/netlify/gocommerce/models"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var serveCmd = cobra.Command{
	Use:  "serve",
	Long: "Start API server",
	Run:  serveCmdFunc,
}

func serveCmdFunc(cmd *cobra.Command, args []string) {
	configFile, err := cmd.Flags().GetString("config")
	if err != nil {
		logrus.Fatalf("%+v", err)
	}

	globalConfig, err := conf.LoadGlobal(configFile)
	if err != nil {
		logrus.Fatalf("Failed to load core configuration: %+v", err)
	}

	if globalConfig.DB.Namespace != "" {
		models.Namespace = globalConfig.DB.Namespace
	}

	config, err := conf.Load(configFile)
	if err != nil {
		logrus.Fatalf("Failed to load instance configuration: %+v", err)
	}

	serve(globalConfig, config)
}

func serve(globalConfig *conf.GlobalConfiguration, config *conf.Configuration) {
	db, err := models.Connect(globalConfig)
	if err != nil {
		logrus.Fatalf("Error opening database: %+v", err)
	}

	bgDB, err := models.Connect(globalConfig)
	if err != nil {
		logrus.Fatalf("Error opening database: %+v", err)
	}

	api := api.NewSingleTenantAPIWithVersion(globalConfig, config, db.Debug(), Version)

	l := fmt.Sprintf("%v:%v", globalConfig.API.Host, globalConfig.API.Port)
	logrus.Infof("GoCommerce API started on: %s", l)

	models.RunHooks(bgDB, logrus.WithField("component", "hooks"))

	api.ListenAndServe(l)
}
