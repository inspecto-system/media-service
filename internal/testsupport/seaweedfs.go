package testsupport

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/inspecto-system/media-service/internal/config"
)

// seaweedImage is the image of platform/deploy/compose.dev.yaml.
const seaweedImage = "chrislusf/seaweedfs:4.52"

// StartSeaweedFS starts SeaweedFS with the S3 gateway and an empty bucket "photos", removed when
// the test ends.
func StartSeaweedFS(t *testing.T) config.Storage {
	t.Helper()
	cfg, stop, err := RunSeaweedFS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return cfg
}

// RunSeaweedFS starts SeaweedFS for a whole test package (from TestMain); call stop at the end.
// Without an S3 identity file the test instance accepts any credentials; access rights per
// service come from the compose configuration.
func RunSeaweedFS(ctx context.Context) (config.Storage, func(), error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        seaweedImage,
			Cmd:          []string{"server", "-dir=/data", "-s3", "-s3.port=8333", "-ip.bind=0.0.0.0", "-master.volumeSizeLimitMB=64"},
			ExposedPorts: []string{"8333/tcp"},
			WaitingFor:   wait.ForHTTP("/").WithPort("8333/tcp").WithStatusCodeMatcher(func(int) bool { return true }),
		},
		Started: true,
	})
	stop := func() { _ = testcontainers.TerminateContainer(container) }
	if err != nil {
		stop()
		return config.Storage{}, nil, fmt.Errorf("start SeaweedFS: %w", err)
	}
	cfg, err := bucketOf(ctx, container)
	if err != nil {
		stop()
		return config.Storage{}, nil, err
	}
	return cfg, stop, nil
}

func bucketOf(ctx context.Context, container testcontainers.Container) (config.Storage, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return config.Storage{}, fmt.Errorf("host: %w", err)
	}
	port, err := container.MappedPort(ctx, "8333/tcp")
	if err != nil {
		return config.Storage{}, fmt.Errorf("port: %w", err)
	}
	cfg := config.Storage{
		Endpoint:  "http://" + host + ":" + port.Port(),
		Region:    "us-east-1",
		Bucket:    "photos",
		AccessKey: "test",
		SecretKey: "test",
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       cfg.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		UsePathStyle: true,
	})
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		return config.Storage{}, fmt.Errorf("create bucket: %w", err)
	}
	return cfg, nil
}
