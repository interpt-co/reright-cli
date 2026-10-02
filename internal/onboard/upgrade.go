package onboard

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/release"
)

// cliBinary is the name of the reright command in release assets.
const cliBinary = "reright"

type UpgradeOptions struct {
	Home       string
	ReleaseURL string
	PublicKey  ed25519.PublicKey
	GOOS       string
	GOARCH     string
	Current    string // version of the running reright, "dev" for a local build
	ExePath    string // the running reright binary, which is replaced
	HookPath   string // empty means the path recorded at install, else the default
	CheckOnly  bool   // only say whether a newer release exists
	Force      bool   // replace the binaries even when the release is not newer
	HTTP       *http.Client
	Out        io.Writer
}

// Latest downloads the signed checksums and returns the version of the newest release. The checksums are
// verified with the compiled-in key before anything in them is read.
func Latest(ctx context.Context, o UpgradeOptions) (string, map[string]string, error) {
	o.defaults()
	if len(o.PublicKey) != ed25519.PublicKeySize {
		return "", nil, errors.New("this build of reright has no release key compiled in, so it cannot verify downloads. Download the official reright binary again")
	}
	table, err := signedTable(ctx, o.HTTP, o.ReleaseURL, o.PublicKey)
	if err != nil {
		return "", nil, err
	}
	raw, err := fetchAsset(ctx, o.HTTP, o.ReleaseURL, table, release.VersionFile, maxTextFetch)
	if err != nil {
		return "", nil, fmt.Errorf("%w (this release predates the upgrade command, so install it again from the dashboard)", err)
	}
	latest := strings.TrimSpace(string(raw))
	if _, ok := release.ParseVersion(latest); !ok {
		return "", nil, fmt.Errorf("the release names a version that cannot be read: %q", latest)
	}
	return latest, table, nil
}

func (o *UpgradeOptions) defaults() {
	if o.ReleaseURL == "" {
		o.ReleaseURL = release.DefaultBaseURL
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
}

// Upgrade replaces reright and reright-hook with the newest signed release. The device token and the agent
// configuration are not touched, so no setup code is needed.
func Upgrade(ctx context.Context, o UpgradeOptions) error {
	o.defaults()
	if !release.Supported(o.GOOS, o.GOARCH) {
		return fmt.Errorf("no reright build for %s/%s (Linux and macOS on amd64 and arm64 only)", o.GOOS, o.GOARCH)
	}
	latest, table, err := Latest(ctx, o)
	if err != nil {
		return err
	}
	hookPath := o.HookPath
	if hookPath == "" {
		hookPath = Paths{o.Home}.DefaultHook()
		if m, _, err := loadManifest(Paths{o.Home}); err == nil && m.HookPath != "" {
			hookPath = m.HookPath
		}
	}
	_, statErr := os.Stat(hookPath)
	hookInstalled := statErr == nil

	// Each program is judged on its own: reright may be current while the hook is older, or too old to say.
	_, known := release.ParseVersion(o.Current)
	cliStale := release.Newer(latest, o.Current) || !known // a local build can always be replaced
	hookSeen := ""
	hookStale := false
	if hookInstalled {
		hookSeen = hookVersion(ctx, hookPath)
		hookStale = hookSeen != latest
	}
	stale := cliStale || hookStale
	if o.CheckOnly {
		switch {
		case cliStale:
			fmt.Fprintf(o.Out, "reright %s is available (you have %s). Run: reright upgrade\n", latest, o.Current)
		case hookStale:
			seen := hookSeen
			if seen == "" {
				seen = "an older build with no version"
			}
			fmt.Fprintf(o.Out, "reright %s is current, but reright-hook is %s and the release is %s. Run: reright upgrade\n", o.Current, seen, latest)
		default:
			fmt.Fprintf(o.Out, "reright %s is up to date.\n", o.Current)
		}
		return nil
	}
	if !stale && !o.Force {
		fmt.Fprintf(o.Out, "reright %s is up to date.\n", o.Current)
		return nil
	}
	if o.ExePath == "" {
		return errors.New("cannot tell where the running reright is, so it cannot be replaced")
	}

	fmt.Fprintf(o.Out, "Downloading reright %s and checking its signed checksums\n", latest)
	cli, err := fetchAsset(ctx, o.HTTP, o.ReleaseURL, table, release.AssetName(cliBinary, o.GOOS, o.GOARCH), maxDownload)
	if err != nil {
		return err
	}
	var hook []byte
	if hookInstalled {
		if hook, err = fetchAsset(ctx, o.HTTP, o.ReleaseURL, table, release.AssetName(hookBinary, o.GOOS, o.GOARCH), maxDownload); err != nil {
			return err
		}
	}

	// The hook goes first: it is what agents run on every call. A failure after it leaves a working pair.
	if hookInstalled {
		if err := writeFile(hookPath, hook, 0o755); err != nil {
			return fmt.Errorf("replace %s: %w", hookPath, err)
		}
		fmt.Fprintf(o.Out, "Replaced %s\n", hookPath)
	} else {
		fmt.Fprintf(o.Out, "reright-hook is not installed at %s, so only reright is replaced. Run reright install to set up your agents.\n", hookPath)
	}
	if err := writeFile(o.ExePath, cli, 0o755); err != nil {
		return fmt.Errorf("replace %s: %w (the hook was already replaced)", o.ExePath, err)
	}
	fmt.Fprintf(o.Out, "Replaced %s\n", o.ExePath)
	fmt.Fprintf(o.Out, "Upgraded to reright %s (was reright %s). Run reright doctor to check the setup.\n", latest, o.Current)
	return nil
}
