package config

import "errors"

var (
	ErrInvalidName       = errors.New("nome do cluster é obrigatório")
	ErrInvalidProvider   = errors.New("provider é obrigatório (aws, azure, gcp)")
	ErrInvalidRegion     = errors.New("região é obrigatória")
	ErrInvalidNodeCount  = errors.New("configuração de nodes inválida (min <= desired <= max)")
	ErrInvalidVPCCidr    = errors.New("CIDR da VPC inválido")
	ErrInvalidK8sVersion = errors.New("versão do Kubernetes inválida")
)
