package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/daemon"
)

// resolveCluster picks the configured cluster a daemon command targets. The
// addressable name is the cluster's kube context (config.Cluster.KubeContext).
// With no arg it defaults to the sole configured cluster; with several it errors
// and lists the candidates so the operator knows what to pass. It is pure over a
// config.Config so the defaulting rules are unit-testable without disk.
func resolveCluster(cfg config.Config, arg string) (config.Cluster, error) {
	if arg != "" {
		for _, c := range cfg.Clusters {
			if c.KubeContext == arg {
				return c, nil
			}
		}
		return config.Cluster{}, fmt.Errorf("no configured cluster with kube context %q; configured: %s",
			arg, contextList(cfg))
	}
	switch len(cfg.Clusters) {
	case 0:
		return config.Cluster{}, fmt.Errorf("no clusters configured; run `kubectl oke bastion init` first")
	case 1:
		return cfg.Clusters[0], nil
	default:
		return config.Cluster{}, fmt.Errorf("which cluster? configured: %s", contextList(cfg))
	}
}

// contextList renders the configured kube contexts for an error message.
func contextList(cfg config.Config) string {
	names := make([]string, len(cfg.Clusters))
	for i, c := range cfg.Clusters {
		names[i] = c.KubeContext
	}
	return strings.Join(names, ", ")
}

// resolveClusterFromDisk loads config.yaml and applies resolveCluster, returning
// the target cluster plus its per-cluster daemon Paths under the default base.
// It is the thin disk-touching wrapper the up/down/status commands share.
func resolveClusterFromDisk(arg string) (config.Cluster, daemon.Paths, error) {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return config.Cluster{}, daemon.Paths{}, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return config.Cluster{}, daemon.Paths{}, err
	}
	cluster, err := resolveCluster(cfg, arg)
	if err != nil {
		return config.Cluster{}, daemon.Paths{}, err
	}
	base, err := daemon.DefaultBase()
	if err != nil {
		return config.Cluster{}, daemon.Paths{}, err
	}
	return cluster, daemon.NewPaths(base, cluster.KubeContext), nil
}

// firstArg returns the optional [cluster] positional, or "" when none was given.
func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// newUpCmd builds `kubectl oke bastion up [cluster]`: spawn the detached daemon
// for the cluster and return immediately (ADR-0009). A second up while a daemon
// is already live is a harmless no-op, so the operator can run it repeatedly.
func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "up [cluster]",
		Short:        "Start the background tunnel daemon for a cluster",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, p, err := resolveClusterFromDisk(firstArg(args))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// Best-effort idempotency: the liveness check and Spawn are not
			// locked, so two near-simultaneous ups can race (last PID write wins).
			// Harmless this slice; revisit when the daemon owns a port (Slice D).
			if pid, err := daemon.ReadPID(p.PID()); err == nil && daemon.Running(pid, nil) {
				_, _ = fmt.Fprintf(out, "daemon for %q already running (pid %d)\n", cluster.KubeContext, pid)
				return nil
			}
			if err := daemon.Spawn(p, cluster.KubeContext); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "started daemon for %q; logs at %s\n", cluster.KubeContext, p.Log())
			return nil
		},
	}
}

// newDownCmd builds `kubectl oke bastion down [cluster]`: SIGTERM the daemon so
// it exits cleanly and removes its own PID file. A missing or stale PID file is
// reported as "not running", not a crash.
func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "down [cluster]",
		Short:        "Stop the background tunnel daemon for a cluster",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, p, err := resolveClusterFromDisk(firstArg(args))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			pid, err := daemon.ReadPID(p.PID())
			if err != nil || !daemon.Running(pid, nil) {
				// Reap a stale PID file left by a crashed daemon so down also
				// clears control-plane litter, not just a clean shutdown.
				_ = daemon.RemovePID(p.PID())
				_, _ = fmt.Fprintf(out, "daemon for %q is not running\n", cluster.KubeContext)
				return nil
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("signalling daemon %d for %q: %w", pid, cluster.KubeContext, err)
			}
			_, _ = fmt.Fprintf(out, "stopped daemon for %q (pid %d)\n", cluster.KubeContext, pid)
			return nil
		},
	}
}

// newStatusCmd builds `kubectl oke bastion status [cluster]`: load the state
// file, check PID liveness, and render via the pure renderer. A stale PID file
// (state says running, process gone) renders as stopped.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status [cluster]",
		Short:        "Report the background tunnel daemon's status for a cluster",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, p, err := resolveClusterFromDisk(firstArg(args))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			pid, perr := daemon.ReadPID(p.PID())
			running := perr == nil && daemon.Running(pid, nil)

			// A never-started daemon has no state file; that is simply stopped,
			// not an error. Any other load failure (corrupt) is surfaced.
			state, serr := daemon.LoadState(p.State())
			if serr != nil {
				// errors.Is (not os.IsNotExist) so the %w-wrapped not-exist from
				// LoadState is recognized: a never-started daemon has no state
				// file and is simply stopped, not an error.
				if errors.Is(serr, os.ErrNotExist) {
					_, _ = fmt.Fprint(out, daemon.RenderStatus(daemon.State{}, false))
					return nil
				}
				return serr
			}
			_, _ = fmt.Fprint(out, daemon.RenderStatus(state, running))
			return nil
		},
	}
}

// newDaemonCmd builds the hidden `__daemon [cluster]` entrypoint that `up`
// re-execs into. It is not user-facing (Hidden) — it runs the daemon body
// (idle until SIGTERM in this slice) under the cluster's per-cluster paths.
func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "__daemon [cluster]",
		Hidden:       true,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			base, err := daemon.DefaultBase()
			if err != nil {
				return err
			}
			return daemon.Run(daemon.NewPaths(base, firstArg(args)))
		},
	}
}
