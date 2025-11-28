package config

type EKSConfig struct {
	ClusterName string
	AWSRegion   string
	Version     string

	NodeType string
	MinNodes int
	MaxNodes int
	UseSpot  bool

	VpcID            string
	PublicSubnetIDs  []string
	PrivateSubnetIDs []string

	ExtraIngressRules []string

	WithLBController bool
	WithNginx        bool
	WithArgoCD       bool
	WithIstio        bool
	WithCertManager  bool

	ArgocdDomain     string
	CertManagerEmail string
	AcmeServer       string
	BaseDomain       string
	WithExternalDNS  bool
	RefreshState     bool
	ACMEProvider     string
	ACMEConfig       *ACMEConfig
	SecretsProvider  string
	KMSKeyID         string
	ConfigPassphrase string
}

type ACMEProvider string

const (
	ACMEProviderLetsEncrypt ACMEProvider = "letsencrypt"
	ACMEProviderGoogle      ACMEProvider = "google"
)

type ACMEConfig struct {
	Provider    ACMEProvider
	Environment string // "production" ou "staging"
}

func (a *ACMEConfig) GetServerURL() string {
	servers := map[ACMEProvider]map[string]string{
		ACMEProviderLetsEncrypt: {
			"production": "https://acme-v02.api.letsencrypt.org/directory",
			"staging":    "https://acme-staging-v02.api.letsencrypt.org/directory",
		},
		ACMEProviderGoogle: {
			"production": "https://dv.acme-v02.api.pki.goog/directory",
			"staging":    "https://dv.acme-v02.test-api.pki.goog/directory",
		},
	}

	if providerServers, ok := servers[a.Provider]; ok {
		if url, ok := providerServers[a.Environment]; ok {
			return url
		}
	}

	// Fallback para Let's Encrypt Production
	return "https://acme-v02.api.letsencrypt.org/directory"
}

func (a *ACMEConfig) GetIssuerName() string {
	names := map[ACMEProvider]string{
		ACMEProviderLetsEncrypt: "letsencrypt",
		ACMEProviderGoogle:      "google-trust",
	}

	if name, ok := names[a.Provider]; ok {
		return name
	}
	return "letsencrypt"
}
