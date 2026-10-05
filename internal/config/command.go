package config

import (
	"errors"
	"flag"
	"fmt"
	"io"

	policyconfig "github.com/truvity/policy/config"

	"github.com/truvity/sluis/internal/version"
)

// Command reads one subcommand's command line, which is `--config <file>` and
// nothing else but `--version` and `--help`; with no --config, SLUIS_CONFIG
// names the file. command is what the person typed to get here ("sluis
// serve"), and schema names the document it reads:
// schemas/config/<schema>.schema.json. A flag that overrides a key, or stands
// in for one, is a second source of truth: the service document, and the policy
// document it names, are the whole of the configuration.
//
// It returns the file to read. done is true when the command line asked for
// something answered here (the version, the help) and the process should stop
// without error.
func Command(command, schema string, args []string, out io.Writer) (file string, done bool, err error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "Usage: %s [--config <file>]\n\n"+
			"%s is configured by one YAML document, validated against schemas/config/%s.schema.json before\n"+
			"anything starts, and the policy document it names (policy.file, schemas/config/policy.schema.json).\n"+
			"The document is --config, or else the file %s names. No other flag or environment variable\n"+
			"configures it, except the secrets the document names and the OTEL_* variables of OpenTelemetry.\n\n",
			command, command, schema, EnvConfig)
		fs.PrintDefaults()
	}
	path := fs.String("config", "", "the service document: the one thing that configures this process (default: $"+EnvConfig+")")
	showVersion := fs.Bool("version", false, "print this build's version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", true, nil
		}
		return "", false, err
	}
	if *showVersion {
		_, _ = fmt.Fprintln(out, command, version.String())
		return "", true, nil
	}
	if fs.NArg() > 0 {
		return "", false, fmt.Errorf("%s takes no arguments: only --config <file>", command)
	}
	if *path != "" {
		return *path, false, nil
	}
	file, err = policyconfig.PathFrom(nil, EnvConfig)
	if err != nil {
		return "", false, fmt.Errorf("%w: it is the only thing that configures this process "+
			"(schemas/config/%s.schema.json says what it holds)", err, schema)
	}
	return file, false, nil
}
