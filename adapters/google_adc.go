package adapters

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const googleCloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
const googleOAuthTokenURL = "https://oauth2.googleapis.com/token"

type googleADCFile struct {
	Type             string `json:"type"`
	ProjectID        string `json:"project_id"`
	QuotaProjectID   string `json:"quota_project_id"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret"`
	RefreshToken     string `json:"refresh_token"`
	ClientEmail      string `json:"client_email"`
	PrivateKey       string `json:"private_key"`
	TokenURI         string `json:"token_uri"`
	SubjectTokenType string `json:"subject_token_type"`
}

func applyGoogleADC(ctx context.Context, model ModelConfig) (ModelConfig, error) {
	prepared := model
	token := ""
	var err error
	if envName := strings.TrimSpace(model.APIKeyEnv); envName != "" && envName != "GOOGLE_APPLICATION_CREDENTIALS" {
		if path := strings.TrimSpace(os.Getenv(envName)); path != "" {
			token, err = googleADCTokenFromFile(ctx, path)
			if err != nil {
				return prepared, err
			}
		}
	}
	if strings.TrimSpace(token) == "" {
		token, err = googleADCAccessToken(ctx)
		if err != nil {
			return prepared, err
		}
	}
	prepared.AuthType = "bearer"
	prepared.AuthHeader = defaultString(prepared.AuthHeader, "Authorization")
	prepared.APIKey = token
	return prepared, nil
}

func googleADCProjectID(ctx context.Context, model ModelConfig) string {
	if model.ProviderOptions != nil {
		for _, key := range []string{"vertex_project_id", "project_id", "project"} {
			if value := strings.TrimSpace(providerOptionString(model, key, "")); value != "" {
				return value
			}
		}
	}
	for _, envName := range []string{"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	seen := map[string]bool{}
	for _, path := range googleADCCredentialCandidatePaths(model) {
		clean := strings.TrimSpace(path)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		if value := googleADCProjectIDFromFile(clean); value != "" {
			return value
		}
	}
	if value := googleADCProjectIDFromGcloud(ctx); value != "" {
		return value
	}
	if value := googleADCProjectIDFromMetadata(ctx); value != "" {
		return value
	}
	return ""
}

func googleADCCredentialCandidatePaths(model ModelConfig) []string {
	paths := []string{}
	if envName := strings.TrimSpace(model.APIKeyEnv); envName != "" {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			paths = append(paths, value)
		}
	}
	if value := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); value != "" {
		paths = append(paths, value)
	}
	if value := defaultGcloudADCPath(); value != "" {
		paths = append(paths, value)
	}
	return paths
}

func googleADCProjectIDFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cred googleADCFile
	if err := json.Unmarshal(data, &cred); err != nil {
		return ""
	}
	for _, value := range []string{cred.ProjectID, cred.QuotaProjectID} {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return ""
}

func googleADCProjectIDFromGcloud(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(out))
	if value == "" || strings.EqualFold(value, "(unset)") {
		return ""
	}
	return value
}

func googleADCProjectIDFromMetadata(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/project/project-id", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.TrimSpace(string(body))
}

func googleADCAccessToken(ctx context.Context) (string, error) {
	if path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); path != "" {
		if token, err := googleADCTokenFromFile(ctx, path); err == nil && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token), nil
		} else if err != nil {
			if token, gcloudErr := googleADCTokenFromGcloud(ctx); gcloudErr == nil && strings.TrimSpace(token) != "" {
				return strings.TrimSpace(token), nil
			}
			return "", err
		}
	}
	if token, err := googleADCTokenFromGcloud(ctx); err == nil && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token), nil
	}
	if path := defaultGcloudADCPath(); path != "" {
		if token, err := googleADCTokenFromFile(ctx, path); err == nil && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token), nil
		}
	}
	if token, err := googleADCTokenFromMetadata(ctx); err == nil && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token), nil
	}
	return "", errors.New("Google ADC credentials were not available. Configure Application Default Credentials with GOOGLE_APPLICATION_CREDENTIALS, run gcloud auth application-default login, use an attached Google Cloud service account, or configure Workload Identity Federation and a supported credential source")
}

func googleADCTokenFromFile(ctx context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Google ADC could not read credential file %q: %w", path, err)
	}
	var cred googleADCFile
	if err := json.Unmarshal(data, &cred); err != nil {
		return "", fmt.Errorf("Google ADC credential file %q was not valid JSON: %w", path, err)
	}
	switch strings.TrimSpace(cred.Type) {
	case "authorized_user":
		return googleADCTokenFromAuthorizedUser(ctx, cred)
	case "service_account":
		return googleADCTokenFromServiceAccount(ctx, cred)
	case "external_account", "external_account_authorized_user":
		if token, err := googleADCTokenFromGcloud(ctx); err == nil && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token), nil
		}
		return "", errors.New("Google ADC found a Workload Identity Federation/external-account credential file. Install/configure gcloud or provide a credential source supported by gcloud so AgentGO can mint a Vertex access token")
	default:
		return "", fmt.Errorf("Google ADC credential file %q has unsupported credential type %q", path, cred.Type)
	}
}

func googleADCTokenFromAuthorizedUser(ctx context.Context, cred googleADCFile) (string, error) {
	if strings.TrimSpace(cred.ClientID) == "" || strings.TrimSpace(cred.ClientSecret) == "" || strings.TrimSpace(cred.RefreshToken) == "" {
		return "", errors.New("Google ADC authorized_user credentials are missing client_id, client_secret, or refresh_token")
	}
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("client_id", strings.TrimSpace(cred.ClientID))
	values.Set("client_secret", strings.TrimSpace(cred.ClientSecret))
	values.Set("refresh_token", strings.TrimSpace(cred.RefreshToken))
	return googleOAuthTokenRequest(ctx, googleOAuthTokenURL, values)
}

func googleADCTokenFromServiceAccount(ctx context.Context, cred googleADCFile) (string, error) {
	if strings.TrimSpace(cred.ClientEmail) == "" || strings.TrimSpace(cred.PrivateKey) == "" {
		return "", errors.New("Google ADC service_account credentials are missing client_email or private_key")
	}
	tokenURL := strings.TrimSpace(cred.TokenURI)
	if tokenURL == "" {
		tokenURL = googleOAuthTokenURL
	}
	assertion, err := googleServiceAccountJWT(cred.ClientEmail, cred.PrivateKey, tokenURL)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	values.Set("assertion", assertion)
	return googleOAuthTokenRequest(ctx, tokenURL, values)
}

func googleServiceAccountJWT(clientEmail, privateKey, tokenURL string) (string, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return "", errors.New("Google ADC service_account private_key was not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if key, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
			parsed = key
		} else {
			return "", fmt.Errorf("Google ADC service_account private_key could not be parsed: %w", err)
		}
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("Google ADC service_account private_key was not an RSA key")
	}
	now := time.Now().Unix()
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   strings.TrimSpace(clientEmail),
		"scope": googleCloudPlatformScope,
		"aud":   tokenURL,
		"iat":   now,
		"exp":   now + 3600,
	}
	encodedHeader, err := jsonBase64URL(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := jsonBase64URL(claims)
	if err != nil {
		return "", err
	}
	signingInput := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("Google ADC service_account JWT signing failed: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func jsonBase64URL(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func googleOAuthTokenRequest(ctx context.Context, tokenURL string, values url.Values) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Google ADC token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("Google ADC token request returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("Google ADC token response was not valid JSON: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		msg := strings.TrimSpace(payload.ErrorDesc)
		if msg == "" {
			msg = strings.TrimSpace(payload.Error)
		}
		if msg == "" {
			msg = "token response did not include access_token"
		}
		return "", errors.New("Google ADC token request failed: " + msg)
	}
	return strings.TrimSpace(payload.AccessToken), nil
}

func googleADCTokenFromGcloud(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gcloud", "auth", "application-default", "print-access-token")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("gcloud ADC access-token command failed: %s", msg)
		}
		return "", err
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gcloud ADC access-token command returned an empty token")
	}
	return token, nil
}

func defaultGcloudADCPath() string {
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "gcloud", "application_default_credentials.json")
		}
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	}
	return ""
}

func googleADCTokenFromMetadata(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token?scopes="+url.QueryEscape(googleCloudPlatformScope), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("metadata ADC token request returned %s", resp.Status)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", errors.New("metadata ADC token response did not include access_token")
	}
	return strings.TrimSpace(payload.AccessToken), nil
}
