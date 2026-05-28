package easyshift

// Deps is the dependency-injection container for all side-effect interfaces.
// Production wiring constructs real implementations; tests construct fakes.
type Deps struct {
	Cmd        CommandRunner
	Download   Downloader
	VM         VMManager
	Net        NetworkProvisioner
	Installer  Installer
	Files      FileServer
	CSR        CSRApprover
	Hostname   HostnameInjector
	Host       HostInspector
	DNS        DNSResolver
	DNSManager DNSManager
	// NewCertIssuer constructs a CertIssuer for the per-cluster ACME
	// settings (email + staging). Function-shaped because per-cluster
	// state can't be baked into a singleton like DNSManager.
	NewCertIssuer func(opts CertIssuerOpts) (CertIssuer, error)
}
