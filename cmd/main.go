package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/checkmarx/2ms/config"

	"sync"
	"time"

	"github.com/checkmarx/2ms/plugins"
	"github.com/checkmarx/2ms/reporting"
	"github.com/checkmarx/2ms/secrets"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var Version = "0.0.0"

const (
	timeSleepInterval = 50
	jsonFormat        = "json"
	yamlFormat        = "yaml"
	sarifFormat       = "sarif"

	tagsFlagName            = "tags"
	logLevelFlagName        = "log-level"
	reportPathFlagName      = "report-path"
	stdoutFormatFlagName    = "stdout-format"
	customRegexRuleFlagName = "regex"
)

var (
	tagsVar            []string
	logLevelVar        string
	reportPathVar      []string
	stdoutFormatVar    string
	customRegexRuleVar []string
)

var rootCmd = &cobra.Command{
	Use:     "2ms",
	Short:   "2ms Secrets Detection",
	Long:    "2ms Secrets Detection: A tool to detect secrets in public websites and communication services.",
	Version: Version,
}

var allPlugins = []plugins.IPlugin{
	&plugins.ConfluencePlugin{},
	&plugins.DiscordPlugin{},
	&plugins.FileSystemPlugin{},
	&plugins.SlackPlugin{},
	&plugins.PaligoPlugin{},
	&plugins.GitPlugin{},
}

var channels = plugins.Channels{
	Items:     make(chan plugins.Item),
	Errors:    make(chan error),
	WaitGroup: &sync.WaitGroup{},
}

var report = reporting.Init()
var secretsChan = make(chan reporting.Secret)

func initLog() {
	switch strings.ToLower(logLevelVar) {
	case "trace":
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "err", "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	case "fatal":
		zerolog.SetGlobalLevel(zerolog.FatalLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

func Execute() {
	cobra.OnInitialize(initLog)
	rootCmd.PersistentFlags().StringSliceVar(&tagsVar, tagsFlagName, []string{"all"}, "select rules to be applied")
	rootCmd.PersistentFlags().StringVar(&logLevelVar, logLevelFlagName, "info", "log level (trace, debug, info, warn, error, fatal)")
	rootCmd.PersistentFlags().StringSliceVar(&reportPathVar, reportPathFlagName, []string{}, "path to generate report files. The output format will be determined by the file extension (.json, .yaml, .sarif)")
	rootCmd.PersistentFlags().StringVar(&stdoutFormatVar, stdoutFormatFlagName, "yaml", "stdout output format, available formats are: json, yaml, sarif")
	rootCmd.PersistentFlags().StringArrayVar(&customRegexRuleVar, customRegexRuleFlagName, []string{}, "custom regexes to apply to the scan, must be valid Go regex")

	rootCmd.PersistentPreRun = preRun
	rootCmd.PersistentPostRun = postRun

	group := "Commands"
	rootCmd.AddGroup(&cobra.Group{Title: group, ID: group})

	for _, plugin := range allPlugins {
		subCommand, err := plugin.DefineCommand(channels)
		subCommand.GroupID = group
		if err != nil {
			log.Fatal().Msg(fmt.Sprintf("error while defining command for plugin %s: %s", plugin.GetName(), err.Error()))
		}
		rootCmd.AddCommand(subCommand)
	}

	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Msg(err.Error())
	}
}

func validateTags(tags []string) {
	for _, tag := range tags {
		if !(strings.EqualFold(tag, "all") || strings.EqualFold(tag, secrets.TagApiKey) || strings.EqualFold(tag, secrets.TagClientId) ||
			strings.EqualFold(tag, secrets.TagClientSecret) || strings.EqualFold(tag, secrets.TagSecretKey) || strings.EqualFold(tag, secrets.TagAccessKey) ||
			strings.EqualFold(tag, secrets.TagAccessId) || strings.EqualFold(tag, secrets.TagApiToken) || strings.EqualFold(tag, secrets.TagAccessToken) ||
			strings.EqualFold(tag, secrets.TagRefreshToken) || strings.EqualFold(tag, secrets.TagPrivateKey) || strings.EqualFold(tag, secrets.TagPublicKey) ||
			strings.EqualFold(tag, secrets.TagEncryptionKey) || strings.EqualFold(tag, secrets.TagTriggerToken) || strings.EqualFold(tag, secrets.TagRegistrationToken) ||
			strings.EqualFold(tag, secrets.TagPassword) || strings.EqualFold(tag, secrets.TagUploadToken) || strings.EqualFold(tag, secrets.TagPublicSecret) ||
			strings.EqualFold(tag, secrets.TagSensitiveUrl) || strings.EqualFold(tag, secrets.TagWebhook)) {
			log.Fatal().Msgf(`invalid filter: %s`, tag)
		}
	}
}

func validateFormat(stdout string, reportPath []string) {
	if !(strings.EqualFold(stdout, yamlFormat) || strings.EqualFold(stdout, jsonFormat) || strings.EqualFold(stdout, sarifFormat)) {
		log.Fatal().Msgf(`invalid output format: %s, available formats are: json, yaml and sarif`, stdout)
	}
	for _, path := range reportPath {

		fileExtension := filepath.Ext(path)
		format := strings.TrimPrefix(fileExtension, ".")
		if !(strings.EqualFold(format, yamlFormat) || strings.EqualFold(format, jsonFormat) || strings.EqualFold(format, sarifFormat)) {
			log.Fatal().Msgf(`invalid report extension: %s, available extensions are: json, yaml and sarif`, format)
		}
	}
}

func preRun(cmd *cobra.Command, args []string) {
	validateTags(tagsVar)

	secrets := secrets.Init(tagsVar)

	if err := secrets.AddRegexRules(customRegexRuleVar); err != nil {
		log.Fatal().Msg(err.Error())
	}

	go func() {
		for {
			select {
			case item := <-channels.Items:
				report.TotalItemsScanned++
				channels.WaitGroup.Add(1)
				go secrets.Detect(secretsChan, item, channels.WaitGroup)
			case secret := <-secretsChan:
				report.TotalSecretsFound++
				report.Results[secret.ID] = append(report.Results[secret.ID], secret)
			case err, ok := <-channels.Errors:
				if !ok {
					return
				}
				log.Fatal().Msg(err.Error())
			}
		}
	}()
}

func postRun(cmd *cobra.Command, args []string) {
	channels.WaitGroup.Wait()

	validateFormat(stdoutFormatVar, reportPathVar)

	cfg := config.LoadConfig("2ms", Version)

	// Wait for last secret to be added to report
	time.Sleep(time.Millisecond * timeSleepInterval)

	// -------------------------------------
	// Show Report
	if report.TotalItemsScanned > 0 {
		report.ShowReport(stdoutFormatVar, cfg)
		if len(reportPathFlagName) > 0 {
			err := report.WriteFile(reportPathVar, cfg)
			if err != nil {
				log.Error().Msgf("Failed to create report file with error: %s", err)
			}
		}
	} else {
		log.Error().Msg("Scan completed with empty content")
		os.Exit(0)
	}

	if report.TotalSecretsFound > 0 {
		os.Exit(1)
	} else {
		os.Exit(0)
	}
}
