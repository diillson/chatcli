// llm/token_manager.go
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TokenManager gerencia a obtenção e renovação de tokens de acesso
type TokenManager struct {
	clientID     string
	clientSecret string
	slugName     string
	tenantName   string
	accessToken  string
	expiresAt    time.Time
	mu           sync.RWMutex
	logger       *zap.Logger
	client       *http.Client
}

// NewTokenManager cria uma nova instância de TokenManager
func NewTokenManager(clientID, clientSecret, slugName, tenantName string, logger *zap.Logger) *TokenManager {
	httpClient := utils.NewHTTPClient(logger, 30*time.Second)
	return &TokenManager{
		clientID:     clientID,
		clientSecret: clientSecret,
		slugName:     slugName,
		tenantName:   tenantName,
		logger:       logger,
		client:       httpClient,
	}
}

func GetEnv(key, defaultValue string, logger *zap.Logger) string {
	value := os.Getenv(key)
	if value == "" {
		logger.Info(fmt.Sprintf("%s não definido, assumindo default: %s", key, defaultValue))
		return defaultValue
	}
	return value
}

func (tm *TokenManager) GetSlugAndTenantName() (string, string) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.slugName, tm.tenantName
}

// Atualiza os valores e força uma nova solicitação de token
func (tm *TokenManager) SetSlugAndTenantName(slugName, tenantName string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.slugName = slugName
	tm.tenantName = tenantName
	tm.accessToken = "" // Limpa o token para forçar a solicitação de um novo na próxima vez
	tm.logger.Info("Valores de slug e tenantName atualizados", zap.String("slugName", slugName), zap.String("tenantName", tenantName))
}

// GetAccessToken retorna o token de acesso válido, renovando-o se necessário
func (tm *TokenManager) GetAccessToken(ctx context.Context) (string, error) {
	tm.mu.RLock()
	token := tm.accessToken
	expiration := tm.expiresAt
	tm.mu.RUnlock()

	if time.Until(expiration) > 60*time.Second && token != "" {
		tm.logger.Debug("Token válido encontrado", zap.Time("expires_at", expiration))
		return token, nil
	}

	tm.logger.Info("Token expirado ou ausente, iniciando renovação")
	return tm.refreshToken(ctx)
}

func (tm *TokenManager) RefreshToken(ctx context.Context) (string, error) {
	return tm.refreshToken(ctx)
}

// refreshToken renova o token de acesso
func (tm *TokenManager) refreshToken(ctx context.Context) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.logger.Info("Renovando o access token...")

	// Monta a URL com os valores atuais de tenantName e slugName
	tokenURL := fmt.Sprintf("https://idm.stackspot.com/%s/oidc/oauth/token", tm.tenantName)
	data := strings.NewReader(fmt.Sprintf(
		"grant_type=client_credentials&client_id=%s&client_secret=%s",
		tm.clientID, tm.clientSecret))

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, data)
	if err != nil {
		tm.logger.Error("Erro ao criar a requisição de token", zap.Error(err))
		return "", fmt.Errorf("erro ao criar a requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := tm.client.Do(req)
	if err != nil {
		tm.logger.Error("Erro ao fazer a requisição de token", zap.Error(err))
		return "", fmt.Errorf("erro ao fazer a requisição: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("falha ao obter o token: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
		tm.logger.Error("Falha na requisição de token", zap.String("response", errMsg))
		return "", fmt.Errorf(errMsg)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		tm.logger.Error("Erro ao decodificar a resposta de token", zap.Error(err))
		return "", fmt.Errorf("erro ao decodificar a resposta: %w", err)
	}

	accessToken, ok := result["access_token"].(string)
	if !ok {
		tm.logger.Error("access_token não encontrado na resposta de token", zap.Any("resultado", result))
		return "", fmt.Errorf("não foi possível obter o access_token")
	}

	expiresIn, ok := result["expires_in"].(float64)
	if !ok {
		tm.logger.Error("expires_in não encontrado na resposta de token", zap.Any("resultado", result))
		return "", fmt.Errorf("não foi possível obter expires_in")
	}

	tm.accessToken = accessToken
	tm.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	tm.logger.Info("Token renovado com sucesso", zap.Time("expires_at", tm.expiresAt))

	return tm.accessToken, nil
}
