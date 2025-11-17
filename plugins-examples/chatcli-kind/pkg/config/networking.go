package config

type NetworkingConfig struct {
	PodSubnet     string
	ServiceSubnet string
	DNSDomain     string
}

func DefaultNetworking() NetworkingConfig {
	return NetworkingConfig{
		PodSubnet:     "10.244.0.0/16",
		ServiceSubnet: "10.96.0.0/12",
		DNSDomain:     "cluster.local",
	}
}

func CustomNetworking(podSubnet, serviceSubnet, dnsDomain string) NetworkingConfig {
	config := DefaultNetworking()

	if podSubnet != "" {
		config.PodSubnet = podSubnet
	}
	if serviceSubnet != "" {
		config.ServiceSubnet = serviceSubnet
	}
	if dnsDomain != "" {
		config.DNSDomain = dnsDomain
	}

	return config
}
