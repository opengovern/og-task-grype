package worker

import (
	"archive/tar"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/net/context"
	"io"
	"net/http"
	"net/url"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AuthConfig struct {
	Auth string `json:"auth,omitempty"`
}

type DockerConfig struct {
	Auths map[string]AuthConfig `json:"auths"`
}

type RegistryType string

const (
	RegistryGHCR RegistryType = "ghcr"
	RegistryECR  RegistryType = "ecr"
	RegistryACR  RegistryType = "acr"
)

type ImageFormat string

const (
	FormatDocker      ImageFormat = "docker"
	FormatOCI         ImageFormat = "oci"
	FormatSingularity ImageFormat = "singularity"
)

type Credentials struct {
	GithubUsername string `json:"github_username"`
	GithubToken    string `json:"github_token"`

	ECRAccountID string `json:"ecr_account_id"`
	ECRRegion    string `json:"ecr_region"`

	ACRLoginServer string `json:"acr_login_server"`
	ACRTenantID    string `json:"acr_tenant_id"`
}

func fetchImage(registryType, output, ociArtifactURI string, creds Credentials) error {
	cfg := DockerConfig{
		Auths: make(map[string]AuthConfig),
	}

	switch RegistryType(registryType) {
	case RegistryGHCR:
		ghcrAuth, err := getGHCRAuth(creds.GithubUsername, creds.GithubToken)
		if err != nil {
			return fmt.Errorf("GHCR error: %v\n", err)
		}
		mergeAuths(cfg.Auths, ghcrAuth)
	case RegistryECR:
		ecrAuth, err := getECRAuth(creds.ECRAccountID, creds.ECRRegion)
		if err != nil {
			return fmt.Errorf("ECR error: %v\n", err)
		}
		mergeAuths(cfg.Auths, ecrAuth)
	case RegistryACR:
		acrAuth, err := getACRAuth(creds.ACRLoginServer, creds.ACRTenantID)
		if err != nil {
			return fmt.Errorf("ACR error: %v\n", err)
		}
		mergeAuths(cfg.Auths, acrAuth)
	default:
		return fmt.Errorf("Unsupported registry type: %s\n", registryType)
	}

	configBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("Error marshaling config to JSON: %v\n", err)
	}

	if output == "" {
		fmt.Println(string(configBytes))
	} else {
		dir := filepath.Dir(output)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("Error creating directory for output file: %v\n", err)
		}

		err = os.WriteFile(output, configBytes, 0600)
		if err != nil {
			return fmt.Errorf("Error writing to output file: %v\n", err)
		}
		fmt.Printf("Credentials written to %s\n", output)
	}

	if ociArtifactURI != "" {
		if err := pullAndCreateDockerArchive(ociArtifactURI, cfg); err != nil {
			return fmt.Errorf("Failed to process %s: %v\n", ociArtifactURI, err)
		} else {
			fmt.Printf("Successfully created image.tar for %s.\n", ociArtifactURI)
		}
	}
	return nil
}

// pullAndCreateDockerArchive pulls the image into memory, extracts config and layers,
// and creates a docker-archive (image.tar) that can be used by Grype.
func pullAndCreateDockerArchive(ociArtifactURI string, cfg DockerConfig) error {
	ctx := context.Background()

	ref, err := registry.ParseReference(ociArtifactURI)
	if err != nil {
		return fmt.Errorf("invalid oci-artifact-uri: %w", err)
	}

	credentialsFunc := auth.CredentialFunc(func(ctx context.Context, host string) (auth.Credential, error) {
		if a, ok := cfg.Auths[host]; ok {
			decoded, err := base64.StdEncoding.DecodeString(a.Auth)
			if err != nil {
				return auth.Credential{}, err
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) != 2 {
				return auth.Credential{}, fmt.Errorf("invalid auth format for %s", host)
			}
			return auth.Credential{
				Username: parts[0],
				Password: parts[1],
			}, nil
		}
		return auth.Credential{}, fmt.Errorf("no credentials for host %s", host)
	})

	authClient := &auth.Client{
		Credential: credentialsFunc,
	}

	repo, err := remote.NewRepository(ref.String())
	if err != nil {
		return fmt.Errorf("failed to create repository object: %w", err)
	}
	repo.Client = authClient

	// Pull the artifact into memory store
	memoryStore := memory.New()

	desc, err := oras.Copy(ctx, repo, ref.Reference, memoryStore, "", oras.DefaultCopyOptions)
	if err != nil {
		return fmt.Errorf("oras pull failed: %w", err)
	}

	// Fetch the manifest content
	rc, err := memoryStore.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer rc.Close()

	manifestContent, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}

	// Parse the manifest as OCI image manifest
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	// Fetch config
	configDesc := manifest.Config
	configRC, err := memoryStore.Fetch(ctx, configDesc)
	if err != nil {
		return fmt.Errorf("failed to fetch config: %w", err)
	}
	defer configRC.Close()
	configBytes, err := io.ReadAll(configRC)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Fetch layers
	var layerFiles []string
	for i, layerDesc := range manifest.Layers {
		layerRC, err := memoryStore.Fetch(ctx, layerDesc)
		if err != nil {
			return fmt.Errorf("failed to fetch layer: %w", err)
		}
		layerBytes, err := io.ReadAll(layerRC)
		layerRC.Close()
		if err != nil {
			return fmt.Errorf("failed to read layer: %w", err)
		}
		layerFileName := fmt.Sprintf("layer%d.tar", i+1)
		if err := os.WriteFile(layerFileName, layerBytes, 0644); err != nil {
			return fmt.Errorf("failed to write layer to disk: %w", err)
		}
		layerFiles = append(layerFiles, layerFileName)
	}

	// Write config.json
	if err := os.WriteFile("config.json", configBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config.json: %w", err)
	}

	// Write manifest.json (Docker format)
	// Docker format manifest.json is an array of objects
	// Example:
	// [
	//   {
	//     "Config":"config.json",
	//     "RepoTags":["ghcr.io/opengovern/steampipe-plugin-aws:v0.1.6"],
	//     "Layers":["layer1.tar","layer2.tar",...]
	//   }
	// ]
	repoTag := ref.String()
	dockerManifest := []map[string]interface{}{
		{
			"Config":   "config.json",
			"RepoTags": []string{repoTag},
			"Layers":   layerFiles,
		},
	}
	dockerManifestBytes, err := json.MarshalIndent(dockerManifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal docker manifest.json: %w", err)
	}
	if err := os.WriteFile("manifest.json", dockerManifestBytes, 0644); err != nil {
		return fmt.Errorf("failed to write manifest.json: %w", err)
	}

	// Create image.tar
	if err := createTar("image.tar", append([]string{"manifest.json", "config.json"}, layerFiles...)); err != nil {
		return fmt.Errorf("failed to create tar: %w", err)
	}

	// Cleanup individual files if desired
	// For now, leave them. Uncomment if cleanup is desired:
	/*
		for _, f := range append([]string{"manifest.json", "config.json"}, layerFiles...) {
			os.Remove(f)
		}
	*/

	return nil
}

func createTar(tarPath string, files []string) error {
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return fmt.Errorf("failed to create tar file: %w", err)
	}
	defer tarFile.Close()

	tw := tar.NewWriter(tarFile)
	defer tw.Close()

	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return fmt.Errorf("failed to stat file %s: %w", file, err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header for %s: %w", file, err)
		}
		header.Name = file
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write header for %s: %w", file, err)
		}
		fh, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", file, err)
		}
		if _, err := io.Copy(tw, fh); err != nil {
			fh.Close()
			return fmt.Errorf("failed to copy file data for %s: %w", file, err)
		}
		fh.Close()
	}

	return nil
}

// GHCR auth: username + PAT
func getGHCRAuth(username, token string) (map[string]AuthConfig, error) {
	if username == "" || token == "" {
		return nil, fmt.Errorf("GHCR requires --gh-username and --gh-token")
	}
	authStr := username + ":" + token
	encoded := base64.StdEncoding.EncodeToString([]byte(authStr))
	return map[string]AuthConfig{
		"ghcr.io": {Auth: encoded},
	}, nil
}

// ECR auth: Use AWS SDK to get Docker credentials
func getECRAuth(accountID, region string) (map[string]AuthConfig, error) {
	if accountID == "" || region == "" {
		return nil, fmt.Errorf("ECR requires --aws-account-id and --region")
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := ecr.NewFromConfig(cfg)
	resp, err := client.GetAuthorizationToken(context.Background(), &ecr.GetAuthorizationTokenInput{
		RegistryIds: []string{accountID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get ECR auth token: %w", err)
	}

	if len(resp.AuthorizationData) == 0 || resp.AuthorizationData[0].AuthorizationToken == nil {
		return nil, fmt.Errorf("no authorization token received from ECR")
	}

	authData := resp.AuthorizationData[0]
	decoded, err := base64.StdEncoding.DecodeString(*authData.AuthorizationToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decode auth token: %w", err)
	}

	registry := *authData.ProxyEndpoint
	registry = strings.TrimPrefix(registry, "https://")

	authStr := string(decoded) // "AWS:<token>"
	encoded := base64.StdEncoding.EncodeToString([]byte(authStr))

	return map[string]AuthConfig{
		registry: {Auth: encoded},
	}, nil
}

// ACR auth: Use azidentity to get AAD token, then exchange for ACR token
func getACRAuth(loginServer, tenantID string) (map[string]AuthConfig, error) {
	if loginServer == "" || tenantID == "" {
		return nil, fmt.Errorf("ACR requires --acr-login-server and --acr-tenant-id")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get default azure credential: %w", err)
	}
	ctx := context.Background()
	aadToken, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}})
	if err != nil {
		return nil, fmt.Errorf("failed to get AAD token: %w", err)
	}

	refreshToken, err := getACRRefreshToken(ctx, loginServer, tenantID, aadToken.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get ACR refresh token: %w", err)
	}

	accessToken, err := getACRAccessToken(ctx, loginServer, refreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get ACR access token: %w", err)
	}

	// username is a placeholder
	authStr := "00000000-0000-0000-0000-000000000000:" + accessToken
	encoded := base64.StdEncoding.EncodeToString([]byte(authStr))
	return map[string]AuthConfig{
		loginServer: {Auth: encoded},
	}, nil
}

func getACRRefreshToken(ctx context.Context, acrService, tenantID, aadAccessToken string) (string, error) {
	formData := url.Values{
		"grant_type":   {"access_token"},
		"service":      {acrService},
		"tenant":       {tenantID},
		"access_token": {aadAccessToken},
	}

	urlStr := fmt.Sprintf("https://%s/oauth2/exchange", acrService)
	respBody, err := httpPostForm(ctx, urlStr, formData)
	if err != nil {
		return "", err
	}
	var response map[string]interface{}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("invalid exchange response: %w", err)
	}
	refreshToken, ok := response["refresh_token"].(string)
	if !ok || refreshToken == "" {
		return "", fmt.Errorf("no refresh_token in ACR exchange response")
	}
	return refreshToken, nil
}

func getACRAccessToken(ctx context.Context, acrService, refreshToken string) (string, error) {
	formData := url.Values{
		"grant_type":    {"refresh_token"},
		"service":       {acrService},
		"refresh_token": {refreshToken},
		"scope":         {"repository:*:pull,push"},
	}

	urlStr := fmt.Sprintf("https://%s/oauth2/token", acrService)
	respBody, err := httpPostForm(ctx, urlStr, formData)
	if err != nil {
		return "", err
	}

	var response map[string]interface{}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("invalid token response: %w", err)
	}
	accessToken, ok := response["access_token"].(string)
	if !ok || accessToken == "" {
		return "", fmt.Errorf("no access_token in response")
	}
	return accessToken, nil
}

func httpPostForm(ctx context.Context, urlStr string, data url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s failed: %w", urlStr, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("non-2xx status: %d body: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func mergeAuths(dst, src map[string]AuthConfig) {
	for k, v := range src {
		dst[k] = v
	}
}
