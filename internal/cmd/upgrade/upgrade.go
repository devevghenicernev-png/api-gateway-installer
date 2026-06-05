// Package upgrade implements `apigw upgrade` — explicit, verified self-update.
//
// We follow gh's UX exactly: never auto-update, always print what's new
// before downloading, surface the verification mode used. Failing
// verification at any step refuses the swap.
package upgrade

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/build"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/selfupdate"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f *cmdutil.Factory

	Repo               string
	Check              bool
	Force              bool
	Yes                bool
	InsecureSkipCosign bool
}

func NewCmdUpgrade(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this binary with the latest GitHub release",
		Long: "Queries GitHub for the latest release, verifies its checksum and\n" +
			"(if cosign is installed) the keyless signature, and atomically swaps\n" +
			"the running binary. Never auto-updates — call this command explicitly.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.Yes, _ = c.Flags().GetBool("yes")
			return run(c, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Repo, "repo", selfupdate.DefaultRepo,
		"GitHub repo to pull the release from")
	cmd.Flags().BoolVar(&opts.Check, "check", false,
		"only check; don't download or swap")
	cmd.Flags().BoolVar(&opts.Force, "force", false,
		"swap even if already on the latest version")
	cmd.Flags().BoolVar(&opts.InsecureSkipCosign, "insecure-skip-cosign", false,
		"fall back to sha256-only when cosign is unavailable (downgrades to checksum-only verification — only use on air-gapped hosts)")
	return cmd
}

func run(cmd *cobra.Command, opts *options) error {
	ios := opts.f.IOStreams
	ctx := cmd.Context()

	rel, err := selfupdate.LatestRelease(ctx, opts.Repo)
	if err != nil {
		return tui.NewError("could not fetch latest release", err.Error()).
			WithFix("apigw upgrade --check", "retry").
			WithDocs("E_UPGRADE_FETCH")
	}

	current := build.Version
	newer := selfupdate.IsNewer(current, rel.TagName)
	verifyMode := "cosign+sha256 (keyless, GH OIDC)"
	switch {
	case !cosignAvailable() && opts.InsecureSkipCosign:
		verifyMode = "sha256-only (--insecure-skip-cosign)"
	case !cosignAvailable():
		verifyMode = "sha256-only (cosign missing — install or pass --insecure-skip-cosign)"
	}

	plan := tui.NewPlan("apigw upgrade").
		Add("Current", current).
		Add("Latest", rel.TagName).
		Add("Asset", selfupdate.AssetPattern(rel.TagName)).
		Add("Verify", verifyMode).
		Add("Repo", opts.Repo)
	if !newer && !opts.Force {
		plan.Add("Status", "already on latest — use --force to reinstall")
	}
	plan.Render(ios.Out, ios)

	if opts.Check {
		return nil
	}

	if !newer && !opts.Force {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("\nNothing to do."))
		return nil
	}

	asset, sums, ok := selfupdate.FindAsset(rel)
	if !ok {
		return tui.NewError(
			"no matching release asset",
			fmt.Sprintf("expected %s + checksums.txt in release %s", selfupdate.AssetPattern(rel.TagName), rel.TagName),
		).WithFix(rel.URL, "open release page manually").
			WithDocs("E_UPGRADE_ASSET")
	}

	if !opts.Yes && ios.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Download and swap?", "", true)
		if err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return err
		}
		if !ok {
			return cmdutil.SilentError
		}
	}

	fmt.Fprintf(ios.Out, "%s downloading %s (%.1f MiB) …\n",
		tui.Styles.Muted.Render("›"),
		asset.Name,
		float64(asset.Size)/(1<<20))

	mode, err := selfupdate.DownloadAndSwap(ctx, asset, sums, opts.Repo, selfupdate.SwapOptions{
		AllowMissingCosign: opts.InsecureSkipCosign,
	})
	if err != nil {
		return tui.NewError("upgrade failed", err.Error()).
			WithFix("apigw upgrade --check", "verify the release manually").
			WithDocs("E_UPGRADE_SWAP")
	}

	fmt.Fprintf(ios.Out, "%s swapped binary (%s verified)\n",
		tui.Styles.Success.Render(tui.GlyphCheck), string(mode))
	fmt.Fprintf(ios.Out, "  %s systemctl restart apigw-dashboard.service\n",
		tui.Styles.Accent.Render("Next:"))
	fmt.Fprintf(ios.Out, "  %s apigw version\n",
		tui.Styles.Accent.Render("Verify:"))
	return nil
}

// cosignAvailable reports whether cosign is on PATH. We import os/exec via
// a tiny helper so the lookup is honest (exec.LookPath returns success on
// any executable, including a fake one — adequate signal here).
func cosignAvailable() bool {
	_, err := lookPath("cosign")
	return err == nil
}
