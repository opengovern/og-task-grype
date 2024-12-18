package task

import (
	"encoding/json"
	"fmt"
	"github.com/opengovern/og-util/pkg/es"
	"github.com/opengovern/og-util/pkg/opengovernance-es-sdk"
	"github.com/opengovern/og-util/pkg/tasks"
	"github.com/opengovern/opencomply/services/tasks/scheduler"
	"go.uber.org/zap"
	"golang.org/x/net/context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func RunTask(ctx context.Context, esClient opengovernance.Client, logger *zap.Logger, request tasks.TaskRequest, response *scheduler.TaskResponse) error {
	var ociArtifactURL, registryType, artifactDigest string
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
	artifactDigest, _ = request.TaskDefinition.Params["artifact_digest"]

	logger.Info("Fetching image", zap.String("image", ociArtifactURL))

	err := fetchImage(registryType, fmt.Sprintf("run-%v", request.TaskDefinition.RunID), ociArtifactURL, getCredsFromParams(request.TaskDefinition.Params))
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

	var grypeOutput GrypeOutput
	err = json.Unmarshal(output, &grypeOutput)

	logger.Info("grypeOutput", zap.Any("grypeOutput", grypeOutput))

	result := OciArtifactVulnerabilities{
		ImageURL:        ociArtifactURL,
		ArtifactDigest:  artifactDigest,
		Vulnerabilities: grypeOutput.Matches,
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

	keys, idx := esResult.KeysAndIndex()
	esResult.EsID = es.HashOf(keys...)
	esResult.EsIndex = idx

	err = sendDataToOpensearch(esClient.ES(), esResult)
	if err != nil {
		return err
	}

	response.Result = []byte(fmt.Sprintf("Response stored in elasticsearch index %s by id: %s", idx, es.HashOf(keys...)))

	return nil
}
