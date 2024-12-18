package task

import (
	"encoding/json"
	"fmt"
	"github.com/opengovern/og-task-grype/results"
	"github.com/opengovern/og-util/pkg/es"
	"github.com/opengovern/og-util/pkg/tasks"
	"github.com/opengovern/opencomply/services/tasks/scheduler"
	"go.uber.org/zap"
	"golang.org/x/net/context"
	"io/ioutil"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func RunTask(ctx context.Context, logger *zap.Logger, request tasks.TaskRequest, response *scheduler.TaskResponse) error {
	rs, err := results.NewResourceSender(request.EsDeliverEndpoint, request.TaskDefinition.RunID, request.UseOpenSearch, logger)
	if err != nil {
		return fmt.Errorf("failed to connect to resource sender: %w", err)
	}

	var ociArtifactURL, registryType string
	if v, ok := request.TaskDefinition.Params["oci_artifact_url"]; ok {
		ociArtifactURL = v
	} else {
		return fmt.Errorf("OCI artifact url parameter is not provided")
	}
	if v, ok := request.TaskDefinition.Params["registry_type"]; ok {
		registryType = v
	} else {
		registryType = "ghcr"
	}

	logger.Info("Fetching image", zap.String("image", ociArtifactURL))

	err = fetchImage(registryType, fmt.Sprintf("run-%v", request.TaskDefinition.RunID), ociArtifactURL, getCredsFromParams(request.TaskDefinition.Params))
	if err != nil {
		logger.Error("failed to fetch image", zap.String("image", ociArtifactURL), zap.Error(err))
		return err
	}

	err = showFiles(fmt.Sprintf("run-%v", request.TaskDefinition.RunID))
	if err != nil {
		logger.Error("failed to show files", zap.Error(err))
		return err
	}

	logger.Info("Scanning image", zap.String("image", "image.tar"))

	// Run the Grype command
	cmd := exec.Command("grype", fmt.Sprintf("run-%v/%s", request.TaskDefinition.RunID, "image.tar"), "-o", "json")

	output, err := cmd.CombinedOutput()
	logger.Info("output", zap.String("output", string(output)))
	if err != nil {
		logger.Error("error running grype script", zap.Error(err))
		return err
	}

	var vulnerabilities []GrypeVulnerabilityMatch
	err = json.Unmarshal(output, &vulnerabilities)

	result := OciArtifactVulnerabilities{
		ImageURL:             ociArtifactURL,
		GrypeVulnerabilities: vulnerabilities,
	}

	esResult := &es.TaskResult{
		PlatformID:   fmt.Sprintf("%s:::%s:::%s", request.TaskDefinition.TaskType, request.TaskDefinition.ResultType, result.UniqueID()),
		ResourceID:   result.UniqueID(),
		ResourceName: ociArtifactURL,
		Description:  result,
		ResultType:   strings.ToLower(request.TaskDefinition.ResultType),
		TaskType:     request.TaskDefinition.TaskType,
		Metadata:     nil,
		DescribedAt:  time.Now().Unix(),
		DescribedBy:  strconv.FormatUint(uint64(request.TaskDefinition.RunID), 10),
	}

	rs.Send(esResult)

	keys, idx := esResult.KeysAndIndex()
	response.Result = []byte(fmt.Sprintf("Response stored in elasticsearch index %s by id: %s", idx, es.HashOf(keys...)))

	return nil
}

func getCredsFromParams(params map[string]string) Credentials {
	creds := Credentials{}
	for k, v := range params {
		switch k {
		case "github_username":
			creds.GithubUsername = v
		case "github_token":
			creds.GithubToken = v
		case "ecr_account_id":
			creds.ECRAccountID = v
		case "ecr_region":
			creds.ECRRegion = v
		case "acr_login_server":
			creds.ACRLoginServer = v
		case "acr_tenant_id":
			creds.ACRTenantID = v
		}
	}
	return creds
}

func showFiles(dir string) error {
	// List the files in the current directory
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	// Print each file or directory name
	fmt.Printf("Listing files in directory: %s\n", dir)
	for _, file := range files {
		if file.IsDir() {
			fmt.Printf("[DIR] %s\n", file.Name())
		} else {
			fmt.Printf("[FILE] %s\n", file.Name())
		}
	}
	return nil
}
