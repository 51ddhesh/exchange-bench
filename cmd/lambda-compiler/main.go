package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"bytes"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/51ddhesh/exchange-bench/internal/compiler"
)

type job struct {
	SubmissionID string `json:"SubmissionID"`
	TeamID       string `json:"TeamID"`
	Attempt      int64  `json:"Attempt"`
	RunID        string `json:"RunID"`
	Language     string `json:"Language"`
	SourcePath   string `json:"SourcePath"`
}

type webhookPayload struct {
	SubmissionID string `json:"submission_id"`
	ArtifactPath string `json:"artifact_path"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

var s3Client *s3.Client
var s3Bucket string
var apiWebhookUrl string

func init() {
	s3Bucket = os.Getenv("S3_BUCKET")
	apiWebhookUrl = os.Getenv("API_WEBHOOK_URL")

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	s3Client = s3.NewFromConfig(cfg)
}

func handler(ctx context.Context, sqsEvent events.SQSEvent) error {
	for _, message := range sqsEvent.Records {
		var j job
		if err := json.Unmarshal([]byte(message.Body), &j); err != nil {
			log.Printf("failed to unmarshal job: %v", err)
			continue
		}

		log.Printf("Compiling submission: %s (%s)", j.SubmissionID, j.Language)
		artifactS3Key, compileErr := compileJob(ctx, j)

		status := "success"
		errorMsg := ""
		if compileErr != nil {
			status = "failed"
			errorMsg = compileErr.Error()
			log.Printf("Compilation failed: %v", compileErr)
		}

		payload := webhookPayload{
			SubmissionID: j.SubmissionID,
			ArtifactPath: artifactS3Key,
			Status:       status,
			Error:        errorMsg,
		}

		payloadBytes, _ := json.Marshal(payload)
		resp, err := http.Post(apiWebhookUrl, "application/json", bytes.NewBuffer(payloadBytes))
		if err != nil {
			log.Printf("Failed to notify webhook: %v", err)
		} else {
			resp.Body.Close()
		}
	}
	return nil
}

func compileJob(ctx context.Context, j job) (string, error) {
	lang, ok := compiler.Lookup(j.Language)
	if !ok {
		return "", fmt.Errorf("unsupported language %q", j.Language)
	}

	workDir := filepath.Join("/tmp", j.SubmissionID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %v", err)
	}
	defer os.RemoveAll(workDir)

	srcExt := filepath.Ext(j.SourcePath)
	if srcExt == "" {
		if len(lang.Extensions) > 0 {
			srcExt = lang.Extensions[0]
		}
	}
	localSrcPath := filepath.Join(workDir, "source"+srcExt)
	localBinPath := filepath.Join(workDir, "binary")

	// 1. Download source from S3
	f, err := os.Create(localSrcPath)
	if err != nil {
		return "", fmt.Errorf("create local source: %v", err)
	}
	out, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s3Bucket),
		Key:    aws.String(j.SourcePath),
	})
	if err != nil {
		f.Close()
		return "", fmt.Errorf("s3 download: %v", err)
	}
	if _, err := io.Copy(f, out.Body); err != nil {
		f.Close()
		out.Body.Close()
		return "", fmt.Errorf("s3 read body: %v", err)
	}
	f.Close()
	out.Body.Close()

	artifactS3Key := j.SourcePath // python stages directly
	if lang.CompileCmd != nil {
		// 2. Compile natively
		args := lang.CompileCmd(localSrcPath, localBinPath)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("compile: %v\n%s", err, output)
		}

		// 3. Upload binary to S3
		binFile, err := os.Open(localBinPath)
		if err != nil {
			return "", fmt.Errorf("open binary: %v", err)
		}
		defer binFile.Close()

		artifactS3Key = fmt.Sprintf("binaries/%s/binary", j.SubmissionID)
		_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s3Bucket),
			Key:    aws.String(artifactS3Key),
			Body:   binFile,
		})
		if err != nil {
			return "", fmt.Errorf("s3 upload binary: %v", err)
		}
	}

	return artifactS3Key, nil
}

func main() {
	lambda.Start(handler)
}
