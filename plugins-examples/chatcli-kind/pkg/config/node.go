package config

type NodeConfig struct {
	Role             string
	Labels           map[string]string
	ExtraPortMapping []PortMapping
}

type PortMapping struct {
	ContainerPort int
	HostPort      int
	Protocol      string
}
