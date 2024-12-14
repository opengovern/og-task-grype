package worker

import (
	"context"
	"encoding/json"
	fmt "fmt"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/opengovern/og-util/pkg/jq"
	"github.com/opengovern/opencomply/services/tasks/db/models"
	"github.com/opengovern/opencomply/services/tasks/scheduler"
	"go.uber.org/zap"
	"os"
	"os/exec"
	"time"
)

var (
	NatsURL         = os.Getenv("NATS_URL")
	NatsConsumer    = os.Getenv("NATS_CONSUMER")
	StreamName      = os.Getenv("NATS_STREAM_NAME")
	TopicName       = os.Getenv("NATS_TOPIC_NAME")
	ResultTopicName = os.Getenv("NATS_RESULT_TOPIC_NAME")
)

type Worker struct {
	logger *zap.Logger
	jq     *jq.JobQueue
}

func NewWorker(
	logger *zap.Logger,
	ctx context.Context,
) (*Worker, error) {
	jq, err := jq.New(NatsURL, logger)
	if err != nil {
		logger.Error("failed to create job queue", zap.Error(err), zap.String("url", NatsURL))
		return nil, err
	}

	if err := jq.Stream(ctx, StreamName, "task job queue", []string{TopicName, ResultTopicName}, 100); err != nil {
		logger.Error("failed to create stream", zap.Error(err))
		return nil, err
	}

	w := &Worker{
		logger: logger,
		jq:     jq,
	}

	return w, nil
}

func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("starting to consume")

	consumeCtx, err := w.jq.ConsumeWithConfig(ctx, NatsConsumer, StreamName, []string{TopicName}, jetstream.ConsumerConfig{
		Replicas:          1,
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		MaxAckPending:     -1,
		AckWait:           time.Minute * 30,
		InactiveThreshold: time.Hour,
	}, []jetstream.PullConsumeOpt{
		jetstream.PullMaxMessages(1),
	}, func(msg jetstream.Msg) {
		w.logger.Info("received a new job")
		w.logger.Info("committing")
		if err := msg.InProgress(); err != nil {
			w.logger.Error("failed to send the initial in progress message", zap.Error(err), zap.Any("msg", msg))
		}
		ticker := time.NewTicker(15 * time.Second)
		go func() {
			for range ticker.C {
				if err := msg.InProgress(); err != nil {
					w.logger.Error("failed to send an in progress message", zap.Error(err), zap.Any("msg", msg))
				}
			}
		}()

		err := w.ProcessMessage(ctx, msg)
		if err != nil {
			w.logger.Error("failed to process message", zap.Error(err))
		}
		ticker.Stop()

		if err := msg.Ack(); err != nil {
			w.logger.Error("failed to send the ack message", zap.Error(err), zap.Any("msg", msg))
		}

		w.logger.Info("processing a job completed")
	})
	if err != nil {
		return err
	}

	w.logger.Info("consuming")

	<-ctx.Done()
	consumeCtx.Drain()
	consumeCtx.Stop()

	return nil
}

func (w *Worker) ProcessMessage(ctx context.Context, msg jetstream.Msg) (err error) {
	var request scheduler.TaskRequest
	if err := json.Unmarshal(msg.Data(), &request); err != nil {
		w.logger.Error("Failed to unmarshal ComplianceReportJob results", zap.Error(err))
		return err
	}

	var response scheduler.TaskResponse

	defer func() {
		if err != nil {
			response.FailureMessage = err.Error()
			response.Status = models.TaskRunStatusFailed
		} else {
			response.Status = models.TaskRunStatusFailed
		}

		responseJson, err := json.Marshal(response)
		if err != nil {
			w.logger.Error("failed to create job result json", zap.Error(err))
			return
		}

		if _, err := w.jq.Produce(ctx, ResultTopicName, responseJson, fmt.Sprintf("task-run-result-%d", request.RunID)); err != nil {
			w.logger.Error("failed to publish job result", zap.String("jobResult", string(responseJson)), zap.Error(err))
		}
	}()

	response.RunID = request.RunID
	response.Status = models.TaskRunStatusInProgress
	responseJson, err := json.Marshal(response)
	if err != nil {
		w.logger.Error("failed to create response json", zap.Error(err))
		return err
	}

	if _, err = w.jq.Produce(ctx, ResultTopicName, responseJson, fmt.Sprintf("task-run-inprogress-%d", request.RunID)); err != nil {
		w.logger.Error("failed to publish job in progress", zap.String("response", string(responseJson)), zap.Error(err))
	}

	var ociArtifactURL, registryType string
	if v, ok := request.Params["oci_artifact_url"]; ok {
		ociArtifactURL = v
	} else {
		return fmt.Errorf("OCI artifact url parameter is not provided")
	}
	if v, ok := request.Params["registry_type"]; ok {
		registryType = v
	} else {
		registryType = "ghcr"
	}

	w.logger.Info("Fetching image", zap.String("image", ociArtifactURL))

	err = fetchImage(registryType, "./image.tar", ociArtifactURL, getCredsFromParams(request.Params))
	if err != nil {
		w.logger.Error("failed to fetch image", zap.String("image", ociArtifactURL), zap.Error(err))
		return err
	}

	w.logger.Info("Scanning image", zap.String("image", "image.tar"))

	// Run the Grype command
	cmd := exec.Command("grype", "docker-archive:./image.tar")

	output, err := cmd.CombinedOutput()
	w.logger.Info("output", zap.String("output", string(output)))
	if err != nil {
		w.logger.Error("error running grype script", zap.Error(err))
		return err
	}

	response.Result = output
	response.RunID = request.RunID
	response.Status = models.TaskRunStatusFinished
	responseJson, err = json.Marshal(response)
	if err != nil {
		w.logger.Error("failed to create response json", zap.Error(err))
		return err
	}

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
