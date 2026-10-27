package proxy

import (
	"context"
	"os/exec"
	"strings"

	"github.com/turin-dev/aven/internal/compat"
)

// runtimeNames is the proxy container, volumes, and in-container socket directory.
// An existing keel-proxy keeps its old names so it is not recreated (it holds :80/:443 and certificates).
type runtimeNames struct {
	Container    string
	WakeVolume   string
	WakeDir      string
	DataVolume   string
	ConfigVolume string
	Legacy       bool
}

func currentNames() runtimeNames {
	return runtimeNames{
		Container: ContainerName, WakeVolume: WakeVolume, WakeDir: WakeDir,
		DataVolume: DataVolume, ConfigVolume: ConfigVolume,
	}
}

func legacyNames() runtimeNames {
	return runtimeNames{
		Container: LegacyContainerName, WakeVolume: LegacyWakeVolume, WakeDir: LegacyWakeDir,
		DataVolume: LegacyDataVolume, ConfigVolume: LegacyConfigVolume, Legacy: true,
	}
}

func dockerOK(ctx context.Context, bin string, args ...string) bool {
	return exec.CommandContext(ctx, bin, args...).Run() == nil
}

// Resolve picks the proxy names for this node. A running keel-proxy (or its
// wake volume, when the new one does not exist yet) keeps the old layout.
func (c *Caddy) Resolve(ctx context.Context) runtimeNames {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()
	if c.haveNames {
		return c.lay
	}
	bin := c.cfg.DockerBin
	n := currentNames()
	switch {
	case dockerOK(ctx, bin, "inspect", LegacyContainerName):
		n = legacyNames()
		compat.NoteLegacyResource("proxy", "container "+LegacyContainerName, "container "+ContainerName)
	default:
		if !dockerOK(ctx, bin, "volume", "inspect", DataVolume) && dockerOK(ctx, bin, "volume", "inspect", LegacyDataVolume) {
			n.DataVolume = LegacyDataVolume
			n.ConfigVolume = LegacyConfigVolume
			compat.NoteLegacyResource("caddy-volume", "volume "+LegacyDataVolume, "volume "+DataVolume)
		}
		if !dockerOK(ctx, bin, "volume", "inspect", WakeVolume) && dockerOK(ctx, bin, "volume", "inspect", LegacyWakeVolume) {
			n.WakeVolume = LegacyWakeVolume
			compat.NoteLegacyResource("wake-volume", "volume "+LegacyWakeVolume, "volume "+WakeVolume)
		}
	}
	c.lay = n
	c.haveNames = true
	return n
}

// socketDir is the in-container directory Caddy dials. Resolve must have run
// for a legacy proxy; otherwise the new directory is used.
func (c *Caddy) socketDir() string {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()
	if c.haveNames && c.lay.WakeDir != "" {
		return c.lay.WakeDir
	}
	return WakeDir
}

func (c *Caddy) wakeDial() string { return "unix/" + c.socketDir() + "/wake.sock" }

// resolveNetwork uses name when that network exists. When only the keel- name
// exists, the proxy joins that bridge instead of a new aven- network.
func (c *Caddy) resolveNetwork(ctx context.Context, name string) string {
	alt := compat.AltResource(name)
	if alt == "" || name == alt {
		return name
	}
	bin := c.cfg.DockerBin
	if bin == "" {
		bin = "docker"
	}
	if dockerOK(ctx, bin, "network", "inspect", name) {
		return name
	}
	if dockerOK(ctx, bin, "network", "inspect", alt) {
		if strings.HasPrefix(name, compat.Prefix+"-") || name == compat.Prefix {
			compat.NoteLegacyResource("network:"+alt, "docker network "+alt, "docker network "+name)
		}
		return alt
	}
	return name
}

func (c *Caddy) adminListenPath() string { return "unix/" + c.socketDir() + "/caddy-admin.sock|0600" }
