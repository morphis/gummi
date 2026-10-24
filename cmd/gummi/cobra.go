package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// This file wires the top-level commands and their nested subcommands onto
// the cobra tree. Cobra owns routing, help, completion AND parsing: each
// command's flags are declared once, in the bind functions at the bottom,
// and its RunE hands the body a cliFlags view of what cobra parsed. See
// flags.go for why there is exactly one declaration site.

// doctorCmd implements `gummi doctor [--json] [--deep]`.
var doctorCmd = &cobra.Command{
	Use:   "doctor [--json] [--deep]",
	Short: "Run a readiness checklist for the workspace",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDoctor(cmdFlags(cmd))
	},
}

// webCmd implements `gummi web`: the board in a browser, hosted by this
// process with the TUI's own model running without a screen.
var webCmd = &cobra.Command{
	Use:   "web [--addr host:port] [--allow-host names] [--tls-cert file --tls-key file] [--tailscale [--ts-hostname name] [--ts-authkey key] [--ts-tls] [--verbose]] [--no-pairing] [--terminal]",
	Short: "Serve the board to a browser",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWeb(cmdFlags(cmd), args)
	},
}

var webPairCmd = &cobra.Command{
	Use:   "pair [--name person]",
	Short: "Print a pairing code from the running web board",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runWebPair(cmdFlags(cmd))
	},
}

var webDevicesCmd = &cobra.Command{
	Use:   "devices [--json]",
	Short: "List the browsers paired with this board, and any waiting to be let in",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runWebDevices(cmdFlags(cmd))
	},
}

var webUnpairCmd = &cobra.Command{
	Use:   "unpair <id> | --all",
	Short: "Revoke a paired browser, or withdraw one waiting to be let in",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWebUnpair(cmdFlags(cmd), args)
	},
}

func init() {
	bindDoctorFlags(doctorCmd.Flags())
	bindWebFlags(webCmd.Flags())
	bindWebPairFlags(webPairCmd.Flags())
	jsonFlag(webDevicesCmd.Flags(), "emit the paired devices as JSON")
	webUnpairCmd.Flags().Bool("all", false, "unpair every device")

	webCmd.AddCommand(webPairCmd, webDevicesCmd, webUnpairCmd)
}

func bindDoctorFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the readiness checklist as JSON (the skill's setup path)")
	fs.Bool("deep", false, "probe per-role model reachability with a live backend turn (TTL-cached)")
}

// bindWebFlags declares `gummi web`'s flags.
func bindWebFlags(fs *pflag.FlagSet) {
	fs.String("addr", "", "address to serve the board on (default "+defaultWebAddr+"; falls back to GUMMI_WEB_ADDR)")
	fs.String("allow-host", "", "comma-separated names the board also answers to, e.g. a reverse proxy's (`tailscale serve`); "+
		"it always answers to its own address, localhost, the --addr name, the certificate's names and the tailnet's")
	fs.String("tls-cert", "", "serve HTTPS with this certificate (PEM; needs --tls-key)")
	fs.String("tls-key", "", "the private key for --tls-cert (PEM)")
	fs.Bool("tailscale", false, "also serve on your tailnet as its own node (embedded tsnet; no tailscaled, no port forwarding)")
	fs.String("ts-hostname", defaultTSHostname, "the board's node name on your tailnet (with --tailscale)")
	fs.String("ts-authkey", "", "tailnet auth key instead of a browser login (with --tailscale); a flag shows in ps, "+
		"so prefer TS_AUTHKEY in the environment, read when the flag is not given")
	fs.Bool("ts-tls", false, "serve HTTPS on 443 with a tailnet certificate (with --tailscale; needs MagicDNS and HTTPS enabled for the tailnet)")
	fs.Bool("verbose", false, "log the tailnet node's own messages (with --tailscale)")
	fs.Bool("no-pairing", false, "serve without pairing, to anything that can reach the listener (refused unless every listener is loopback)")
	fs.Bool("terminal", false, "give paired devices a Terminal tab: a shell in each card's worktree, running as you (refused with --no-pairing)")
}

func bindWebPairFlags(fs *pflag.FlagSet) {
	fs.String("name", "", "pair the browser that redeems the code as this person")
}
