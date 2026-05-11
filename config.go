package easyshift

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirupsen/logrus"
)

const (
	// Core configuration constants
	DefaultRelConfigDir = ".config/easyshift" // config dir relative to the base (home) directory
	DefaultLogFile      = "easyshift.log"
	DefaultWebPort      = 9393
	DefaultClustersMax  = 3
	DefaultWorkersMax   = 3

	// Network configuration
	BaseNetworkRange = "192.168.1"
	NetworkStart     = 5
	NetworkEnd       = 20

	// OpenShift configuration
	DefaultOCPVersion = "4.14.0"
	OCPMirrorURL      = "https://mirror.openshift.com/pub/openshift-v4/x86_64"
)

// Config represents the global configuration for easyshift
type Config struct {
	sync.RWMutex
	ConfigDir   string           `json:"configDir"`
	LogFile     string           `json:"logFile"`
	WebPort     int              `json:"webPort"`
	Debug       bool             `json:"debug"`
	PullSecret  string           `json:"pullSecret"`
	Clusters    []*ClusterConfig `json:"clusters"`
	GlobalState *GlobalState     `json:"globalState"`
}

// ClusterConfig represents the configuration for a single cluster
type ClusterConfig struct {
	Name          string   `json:"name"`
	Domain        string   `json:"domain"`
	OCPVersion    string   `json:"ocpVersion"`
	MasterCount   int      `json:"masterCount"`
	WorkerCount   int      `json:"workerCount"`
	MasterRAM     int      `json:"masterRAM"`
	WorkerRAM     int      `json:"workerRAM"`
	MasterCPUs    int      `json:"masterCPUs"`
	WorkerCPUs    int      `json:"workerCPUs"`
	MasterDiskGB  int      `json:"masterDiskGB"`
	WorkerDiskGB  int      `json:"workerDiskGB"`
	NetworkSubnet string   `json:"networkSubnet"`
	IPAddresses   []string `json:"ipAddresses"`
	MACAddresses  []string `json:"macAddresses"`
	State         string   `json:"state"`
}

// GlobalState tracks the global state of easyshift
type GlobalState struct {
	UsedIPs       map[string]bool `json:"usedIPs"`
	UsedMACs      map[string]bool `json:"usedMACs"`
	ActiveCluster string          `json:"activeCluster"`
}

var (
	globalConfig *Config
	configOnce   sync.Once
)

// GetConfig returns the singleton config instance
func GetConfig() *Config {
	configOnce.Do(func() {
		defaultConfigDir := filepath.Join(os.TempDir(), "easyshift")

		homeDir, err := os.UserHomeDir()
		if err != nil {
			logrus.Warnf("failed to determine user home directory, using fallback config dir %q: %v", defaultConfigDir, err)
		} else {
			defaultConfigDir = filepath.Join(homeDir, DefaultRelConfigDir)
		}

		globalConfig = &Config{
			ConfigDir: defaultConfigDir,
			LogFile:   filepath.Join(defaultConfigDir, DefaultLogFile),
			WebPort:   DefaultWebPort,
			GlobalState: &GlobalState{
				UsedIPs:  make(map[string]bool),
				UsedMACs: make(map[string]bool),
			},
		}
		if err := globalConfig.load(); err != nil {
			// log the error during initialization but don't panic here
			logrus.Debugf("failed to load config: %v", err)
		}
	})
	return globalConfig
}

// load reads the configuration from disk
func (c *Config) load() error {
	c.Lock()
	defer c.Unlock()

	if err := os.MkdirAll(c.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configFile := filepath.Join(c.ConfigDir, "config.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return c.save() // Save default configuration
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := json.Unmarshal(data, c); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	return nil
}

// save writes the configuration to disk
func (c *Config) save() error {
	c.Lock()
	defer c.Unlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	configFile := filepath.Join(c.ConfigDir, "config.json")
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// InitLogging initializes the logging system
func InitLogging(debug bool) {
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}

	if err := os.MkdirAll(filepath.Dir(GetConfig().LogFile), 0o700); err != nil {
		logrus.Fatal(err)
	}

	logFile, err := os.OpenFile(
		GetConfig().LogFile,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o600,
	)
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.SetOutput(logFile)
}
