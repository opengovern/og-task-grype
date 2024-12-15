package worker

import (
	"context"
	"encoding/json"
	fmt "fmt"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/opencomply/og-task-template/task"
	"github.com/opengovern/og-util/pkg/jq"
	"github.com/opengovern/opencomply/services/tasks/db/models"
	"github.com/opengovern/opencomply/services/tasks/scheduler"
	"github.com/opengovern/opencomply/services/tasks/worker/consts"
	"go.uber.org/zap"
	"os"
	"time"
)

var (
	NatsURL         = os.Getenv(consts.NatsURLEnv)
	NatsConsumer    = os.Getenv(consts.NatsConsumerEnv)
	StreamName      = os.Getenv(consts.NatsStreamNameEnv)
	TopicName       = os.Getenv(consts.NatsTopicNameEnv)
	ResultTopicName = os.Getenv(consts.NatsResultTopicNameEnv)
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

	var response *scheduler.TaskResponse

	defer func() {
		if err != nil {
			response.FailureMessage = err.Error()
			response.Status = models.TaskRunStatusFailed
		} else {
			response.Status = models.TaskRunStatusFinished
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

	err = task.RunTask(ctx, w.logger, request, response)
	if err != nil {
		w.logger.Error("failed to publish job result", zap.String("response", string(responseJson)), zap.Error(err))
		return err
	}
	response.Status = models.TaskRunStatusFinished

	return nil
}
