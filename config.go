package easyshift

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultRelConfigDir = ".config/easyshift"
	DefaultLogFile      = "easyshift.log"
	DefaultPullSecret   = "pull-secret"
	DefaultWebPort      = 9393
	DefaultClustersMax  = 3
	DefaultWorkersMax   = 3

	BaseNetworkRange = "192.168.1"
	NetworkStart     = 5
	NetworkEnd       = 20

	// DefaultOCPVersion is a channel alias, not a pinned version. When set,
	// ClusterManager.Create resolves it to the actual current version by
	// fetching release.txt from the mirror. Users who want a specific
	// release pass --version 4.21.0 (or whatever) on the CLI.
	DefaultOCPVersion = OCPChannelStable
	// OCPChannelStable is the mirror alias for the latest stable z-stream.
	OCPChannelStable = "stable"
	OCPMirrorURL     = "https://mirror.openshift.com/pub/openshift-v4/x86_64"

	// DefaultStoragePool is libvirt's conventional auto-created pool name.
	// The pool name is just a label; on some hosts the pool at
	// /var/lib/libvirt/images is named differently (e.g. "images"), so this
	// is overridable per cluster via --storage-pool.
	DefaultStoragePool = "default"

	// NetworkModeNAT puts cluster VMs behind a libvirt NAT network. The host
	// will (eventually) run HAProxy + DNSmasq to expose the cluster.
	NetworkModeNAT = "nat"
	// NetworkModeBridge attaches each VM to an existing Linux bridge on the
	// host (e.g. br0) that is connected to the LAN. VMs get LAN IPs from the
	// upstream router and can reach the host. No HAProxy is required; the
	// user is responsible for the bridge, a DHCP reservation, and DNS records
	// pointing api.<name>.<domain> and *.apps.<name>.<domain> at the master.
	NetworkModeBridge = "bridge"
)

// Config is the global on-disk configuration. There is no singleton; callers
// construct one via LoadConfig and pass it to managers. The pull secret is
// NOT stored here — see pullsecret.go for its dedicated on-disk file.
type Config struct {
	ConfigDir   string           `json:"configDir"`
	LogFile     string           `json:"logFile"`
	WebPort     int              `json:"webPort"`
	Debug       bool             `json:"debug"`
	Clusters    []*ClusterConfig `json:"clusters"`
	GlobalState *GlobalState     `json:"globalState"`
}

// ClusterConfig is the persisted spec for one cluster.
type ClusterConfig struct {
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	OCPVersion   string `json:"ocpVersion"`
	MasterCount  int    `json:"masterCount"`
	WorkerCount  int    `json:"workerCount"`
	MasterRAM    int    `json:"masterRAM"`
	WorkerRAM    int    `json:"workerRAM"`
	MasterCPUs   int    `json:"masterCPUs"`
	WorkerCPUs   int    `json:"workerCPUs"`
	MasterDiskGB int    `json:"masterDiskGB"`
	WorkerDiskGB int    `json:"workerDiskGB"`

	// NetworkMode is "nat" or "bridge". See the corresponding constants.
	NetworkMode string `json:"networkMode"`
	// Bridge is the name of the existing host Linux bridge VMs attach to in
	// bridge mode (e.g. "br0"). Ignored when NetworkMode == "nat".
	Bridge string `json:"bridge,omitempty"`
	// MasterMAC is the MAC the user reserved at their router for the master
	// VM in bridge mode. Required in bridge mode; ignored in NAT mode.
	MasterMAC string `json:"masterMAC,omitempty"`
	// MasterIP is the IP the user's router will hand out to MasterMAC.
	// Required in bridge mode; ignored in NAT mode.
	MasterIP string `json:"masterIP,omitempty"`
	// MachineCIDR feeds networking.machineNetwork in install-config.yaml. In
	// NAT mode this is auto-derived from the libvirt subnet; in bridge mode
	// it is derived from MasterIP as /24 unless the user overrides it.
	MachineCIDR string `json:"machineCIDR,omitempty"`
	// StoragePool is the libvirt pool where the master disk and boot ISO are
	// created. Defaults to "default"; override with --storage-pool when your
	// host's pool has a different name.
	StoragePool string `json:"storagePool,omitempty"`
	// DNSProvider, if set, names the public DNS backend easyshift will use
	// to create the cluster's A records (api, api-int, *.apps). When
	// empty, DNS records are the user's responsibility — the preflight
	// still verifies they resolve correctly. Persisted so Delete picks the
	// right backend when tearing down a cluster.
	DNSProvider string `json:"dnsProvider,omitempty"`
	// DNSZone is the parent DNS zone that owns the cluster's records. When
	// empty, defaults to Domain (the base domain). Override when the zone
	// is a parent of the base domain, e.g. Domain="dev.rtalur.dev" but the
	// Cloudflare zone is "rtalur.dev".
	DNSZone string `json:"dnsZone,omitempty"`
	// TLSEmail, when non-empty, enables Let's Encrypt cert issuance for
	// api.<fqdn> and *.apps.<fqdn> via ACME DNS-01. Requires DNSProvider
	// to be set (the same provider+token are reused for the challenge).
	// The email is the ACME account email.
	TLSEmail string `json:"tlsEmail,omitempty"`
	// TLSStaging, when true, points ACME at Let's Encrypt's staging
	// endpoint. Useful while iterating because staging has much higher
	// rate limits, but issues certs signed by an untrusted root.
	TLSStaging bool `json:"tlsStaging,omitempty"`

	NetworkSubnet string   `json:"networkSubnet"`
	IPAddresses   []string `json:"ipAddresses"`
	MACAddresses  []string `json:"macAddresses"`
	State         string   `json:"state"`

	// BootISOVolPath is the libvirt default-pool path of the master's
	// bootstrap-in-place ISO, set by the embed-ignition-iso stage and used
	// by create-master-vms. Persisted so a resumed build reuses it.
	BootISOVolPath string `json:"bootISOVolPath,omitempty"`
}

// GlobalState tracks resources allocated across all clusters on this host.
type GlobalState struct {
	UsedIPs       map[string]bool `json:"usedIPs"`
	UsedMACs      map[string]bool `json:"usedMACs"`
	ActiveCluster string          `json:"activeCluster"`
}

// DefaultConfigDir returns the absolute path of the default config dir
// (~/.config/easyshift, or a temp fallback if HOME is unset).
func DefaultConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, DefaultRelConfigDir)
	}
	return filepath.Join(os.TempDir(), "easyshift")
}

// NewDefaultConfig returns a Config populated with default paths and ports,
// rooted at configDir. It does not touch the filesystem.
func NewDefaultConfig(configDir string) *Config {
	return &Config{
		ConfigDir: configDir,
		LogFile:   filepath.Join(configDir, DefaultLogFile),
		WebPort:   DefaultWebPort,
		GlobalState: &GlobalState{
			UsedIPs:  map[string]bool{},
			UsedMACs: map[string]bool{},
		},
	}
}

// LoadConfig reads the config from configDir/config.json. If the file does
// not exist, a default config is written and returned.
func LoadConfig(configDir string) (*Config, error) {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	cfg := NewDefaultConfig(configDir)
	configFile := filepath.Join(configDir, "config.json")
	data, err := os.ReadFile(configFile)
	if errors.Is(err, os.ErrNotExist) {
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("write default config: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.GlobalState == nil {
		cfg.GlobalState = &GlobalState{UsedIPs: map[string]bool{}, UsedMACs: map[string]bool{}}
	}
	if cfg.GlobalState.UsedIPs == nil {
		cfg.GlobalState.UsedIPs = map[string]bool{}
	}
	if cfg.GlobalState.UsedMACs == nil {
		cfg.GlobalState.UsedMACs = map[string]bool{}
	}
	return cfg, nil
}

// Save writes the config to configDir/config.json.
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	configFile := filepath.Join(c.ConfigDir, "config.json")
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
