package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/daemon"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
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
// the target cluster, the configured auth spec (the profile+method init chose),
// and the cluster's per-cluster daemon Paths under the default base. It is the
// thin disk-touching wrapper the up/down/status/__daemon commands share. The
// auth spec is the default the runner uses unless `up` overrides it for CI.
func resolveClusterFromDisk(arg string) (config.Cluster, ociauth.Spec, daemon.Paths, error) {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return config.Cluster{}, ociauth.Spec{}, daemon.Paths{}, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return config.Cluster{}, ociauth.Spec{}, daemon.Paths{}, err
	}
	cluster, err := resolveCluster(cfg, arg)
	if err != nil {
		return config.Cluster{}, ociauth.Spec{}, daemon.Paths{}, err
	}
	base, err := daemon.DefaultBase()
	if err != nil {
		return config.Cluster{}, ociauth.Spec{}, daemon.Paths{}, err
	}
	spec := ociauth.Spec{Method: cfg.Method, Profile: cfg.Profile}
	return cluster, spec, daemon.NewPaths(base, cluster.KubeContext), nil
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
//
// --foreground runs the same supervisor assembly in the foreground (Ctrl-C to
// tear down) instead of detaching, for debugging — it shares runTunnel with the
// __daemon path. --profile and --instance-principal override the configured
// auth so CI can run without interactive init; they apply to both paths — the
// foreground runner uses them directly, and the detached path threads them into
// the __daemon re-exec argv so the background daemon honors them too.
func newUpCmd() *cobra.Command {
	var (
		foreground        bool
		profile           string
		instancePrincipal bool
	)
	cmd := &cobra.Command{
		Use:          "up [cluster]",
		Short:        "Start the background tunnel daemon for a cluster",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, spec, p, err := resolveClusterFromDisk(firstArg(args))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if foreground {
				spec = applyAuthOverrides(spec, profile, instancePrincipal)
				_, _ = fmt.Fprintf(out,
					"running tunnel for %q in the foreground; Ctrl-C to tear down\n",
					cluster.KubeContext)
				ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
				defer stop()
				return runTunnel(ctx, cluster, p, spec, out)
			}

			// Best-effort idempotency: the liveness check and Spawn are not
			// locked, so two near-simultaneous ups can race (last PID write wins).
			if pid, err := daemon.ReadPID(p.PID()); err == nil && daemon.Running(pid, nil) {
				_, _ = fmt.Fprintf(out, "daemon for %q already running (pid %d)\n", cluster.KubeContext, pid)
				return nil
			}
			// Thread the CI auth overrides into the re-exec so the detached
			// daemon honors them: __daemon re-resolves auth from config.yaml,
			// then applies whatever flags it was spawned with.
			if err := daemon.Spawn(p, daemonArgs(cluster.KubeContext, profile, instancePrincipal)...); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "started daemon for %q; logs at %q\n", cluster.KubeContext, p.Log())
			return nil
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false,
		"run the tunnel in the foreground (debugging) instead of detaching")
	cmd.Flags().StringVar(&profile, "profile", "",
		"override the configured OCI profile (for CI)")
	cmd.Flags().BoolVar(&instancePrincipal, "instance-principal", false,
		"authenticate as the OCI compute instance, overriding the configured method (for CI)")
	return cmd
}

// daemonArgs builds the argv `up` re-execs __daemon with: the cluster key
// positional, plus the CI auth-override flags so a detached daemon honors them
// just like the foreground runner. It is pure so the argv construction is
// unit-testable without an actual re-exec (Spawn itself stays integration).
func daemonArgs(clusterKey, profile string, instancePrincipal bool) []string {
	args := []string{clusterKey}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if instancePrincipal {
		args = append(args, "--instance-principal")
	}
	return args
}

// applyAuthOverrides folds the CI auth flags onto the configured auth spec: a
// non-empty profile replaces the profile, and instancePrincipal switches the
// method (instance_principal ignores the profile). Absent flags leave the
// configured spec untouched, so the common case still uses what init chose.
func applyAuthOverrides(spec ociauth.Spec, profile string, instancePrincipal bool) ociauth.Spec {
	if instancePrincipal {
		spec.Method = ociauth.InstancePrincipal
	}
	if profile != "" {
		spec.Profile = profile
	}
	return spec
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
			cluster, _, p, err := resolveClusterFromDisk(firstArg(args))
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
			_, _, p, err := resolveClusterFromDisk(firstArg(args))
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
// re-execs into. It is not user-facing (Hidden). It resolves the cluster from
// config.yaml, applies any CI auth overrides `up` threaded into its argv, writes
// a starting state, then runs the shared supervisor assembly (runTunnel) under a
// SIGTERM/SIGINT-cancellable context — the same code path `up --foreground`
// uses. The supervisor's deferred teardown deletes the session and unwires the
// -bastion context on cancel; the ephemeral key is in-process and dropped when
// this process exits. On return the PID file is removed so a later status reads
// not-running. The --profile/--instance-principal flags mirror up's; they exist
// only so the detached daemon honors CI overrides and carry no UX cost (Hidden).
func newDaemonCmd() *cobra.Command {
	var (
		profile           string
		instancePrincipal bool
	)
	cmd := &cobra.Command{
		Use:          "__daemon [cluster]",
		Hidden:       true,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, spec, p, err := resolveClusterFromDisk(firstArg(args))
			if err != nil {
				return err
			}
			spec = applyAuthOverrides(spec, profile, instancePrincipal)
			out := cmd.OutOrStdout() // → the per-cluster log file (Spawn redirects stdio)

			// Record starting before any tunnel work, so a status between spawn
			// and the first Wire reports starting, not a stale/never-started file.
			if serr := daemon.SaveState(p.State(), daemon.State{
				Phase:     daemon.PhaseStarting,
				StartedAt: time.Now(),
			}); serr != nil {
				return serr
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()

			runErr := runTunnel(ctx, cluster, p, spec, out)
			// Always drop the PID file on exit so down/status see not-running,
			// even if the run errored; surface the run error to the log.
			if rmErr := daemon.RemovePID(p.PID()); rmErr != nil && runErr == nil {
				return rmErr
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "",
		"override the configured OCI profile (threaded in by up; for CI)")
	cmd.Flags().BoolVar(&instancePrincipal, "instance-principal", false,
		"authenticate as the OCI compute instance (threaded in by up; for CI)")
	return cmd
}
