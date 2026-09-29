package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/hub"
	_ "github.com/henrygd/beszel/internal/migrations"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/spf13/cobra"
)

func main() {
	// handle health check first to prevent unneeded execution
	if len(os.Args) > 3 && os.Args[1] == "health" {
		url := os.Args[3]
		if err := checkHealth(url); err != nil {
			log.Fatal(err)
		}
		fmt.Print("ok")
		return
	}

	// in the container image, continue as an unprivileged user
	dropPrivileges()

	baseApp := getBaseApp()
	hub := hub.NewHub(baseApp)
	if err := hub.StartHub(); err != nil {
		log.Fatal(err)
	}
}

// getBaseApp creates a new PocketBase app with the default config
func getBaseApp() *pocketbase.PocketBase {
	isDev := os.Getenv("ENV") == "dev"

	baseApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: beszel.AppName + "_data",
		DefaultDev:     isDev,
	})
	baseApp.RootCmd.Version = beszel.Version
	baseApp.RootCmd.Use = beszel.AppName
	baseApp.RootCmd.Short = ""
	// add update command
	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Update InfraScope to the latest release",
		Run:   hub.Update,
	}
	// The gh.beszel.dev mirror served upstream Beszel binaries, so it is no
	// longer used. The flag is kept (hidden, no-op) so existing scripts work.
	updateCmd.Flags().Bool("china-mirrors", false, "No longer used")
	_ = updateCmd.Flags().MarkDeprecated("china-mirrors", chinaMirrorsDeprecation)
	baseApp.RootCmd.AddCommand(updateCmd)
	// add health command
	baseApp.RootCmd.AddCommand(newHealthCmd())

	// enable auto creation of migration files when making collection changes in the Admin UI
	migratecmd.MustRegister(baseApp, baseApp.RootCmd, migratecmd.Config{
		Automigrate: isDev,
		Dir:         "../../migrations",
	})

	return baseApp
}

const chinaMirrorsDeprecation = "the gh.beszel.dev mirror served upstream Beszel binaries and is no longer used; updates are downloaded from GitHub (AxiomOperator/infrascope)"

func newHealthCmd() *cobra.Command {
	var baseURL string

	healthCmd := &cobra.Command{
		Use:   "health",
		Short: "Check health of running hub",
		Run: func(cmd *cobra.Command, args []string) {
			if err := checkHealth(baseURL); err != nil {
				log.Fatal(err)
			}
			os.Exit(0)
		},
	}
	healthCmd.Flags().StringVar(&baseURL, "url", "", "base URL")
	healthCmd.MarkFlagRequired("url")
	return healthCmd
}

// checkHealth checks the health of the hub.
func checkHealth(baseURL string) error {
	client := &http.Client{
		Timeout: time.Second * 3,
	}
	healthURL := baseURL + "/api/health"
	resp, err := client.Get(healthURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("%s returned status %d", healthURL, resp.StatusCode)
	}
	return nil
}
